// Package wifi runs the head unit's radio as an access point (hostapd +
// dnsmasq) or as a client of someone else's network (wpa_supplicant +
// udhcpc), from a directory of binaries a stage packs under Dir/bin and
// Dir/lib. The radio does one at a time: scanning and joining stop the
// access point.
package wifi

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	ctlDir  = "/tmp/wpa"
	apConf  = "/tmp/hostapd.conf"
	staConf = "/tmp/wpa_supplicant.conf"
	script  = "/tmp/udhcpc.sh"
)

type Runner struct {
	// Dir holds bin/ and lib/ of the AP and client tools.
	Dir   string
	Iface string
	// Address of the access point's /24, and its network's SSID and key.
	APAddr, SSID, PSK string
}

type Network struct {
	SSID    string `json:"ssid"`
	Signal  int    `json:"signal"` // dBm
	Secured bool   `json:"secured"`
}

type Status struct {
	Mode  string `json:"mode"` // "ap", "client" or "off"
	SSID  string `json:"ssid"`
	IP    string `json:"ip"`
	State string `json:"state"` // wpa_supplicant's, in client mode
}

// HasTools: the stage carries the Wi-Fi tools. The radio itself may not be
// there yet (its driver loads after the stage starts) or at all.
func (r Runner) HasTools() bool {
	_, err := os.Stat(r.Dir + "/bin/wpa_supplicant")
	return err == nil
}

// Available: the tools and the radio are there.
func (r Runner) Available() bool {
	if !r.HasTools() {
		return false
	}
	_, err := os.Stat("/sys/class/net/" + r.Iface)
	return err == nil
}

func (r Runner) cmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(r.Dir+"/bin/"+name, args...)
	c.Env = append(os.Environ(), "LD_LIBRARY_PATH="+r.Dir+"/lib")
	return c
}

// ToolLog collects what the tools print, for the stage's Log screen.
const ToolLog = "/tmp/wifi-tools.log"

func (r Runner) out(name string, args ...string) (string, error) {
	b, err := r.cmd(name, args...).CombinedOutput()
	res := strings.TrimSpace(string(b))
	logTool(name, args, res, err)
	// A command that could not run prints nothing: say why.
	if res == "" && err != nil {
		res = err.Error()
	}
	return res, err
}

// logTool appends one run to ToolLog. A key given to wpa_cli is never
// written.
func logTool(name string, args []string, out string, err error) {
	shown := append([]string(nil), args...)
	for i, a := range shown {
		if a == "psk" && i+1 < len(shown) {
			shown[i+1] = "<key>"
		}
	}
	f, ferr := os.OpenFile(ToolLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "$ %s %s\n", name, strings.Join(shown, " "))
	if out != "" {
		fmt.Fprintf(f, "%s\n", out)
	}
	if err != nil {
		fmt.Fprintf(f, "-> %v\n", err)
	}
}

func (r Runner) busybox(args ...string) (string, error) { return r.out("busybox", args...) }

func (r Runner) cli(args ...string) (string, error) {
	return r.out("wpa_cli", append([]string{"-p", ctlDir, "-i", r.Iface}, args...)...)
}

// kill stops the process of pidfile and returns its pid, 0 if none.
func kill(pidfile string) int {
	b, err := os.ReadFile(pidfile)
	if err != nil {
		return 0
	}
	defer os.Remove(pidfile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	syscall.Kill(pid, syscall.SIGTERM)
	return pid
}

// stopAll stops everything on the radio and waits for it to let go: a
// supplicant cannot take the interface while hostapd still has it in AP mode.
func (r Runner) stopAll() {
	var pids []int
	for _, f := range []string{"/tmp/hostapd.pid", "/tmp/dnsmasq.pid", "/tmp/wpa.pid", "/tmp/udhcpc.pid"} {
		if pid := kill(f); pid > 0 {
			pids = append(pids, pid)
		}
	}
	for _, pid := range pids {
		for i := 0; i < 40 && syscall.Kill(pid, 0) == nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	r.busybox("ifconfig", r.Iface, "0.0.0.0")
	r.busybox("ifconfig", r.Iface, "down")
}

// tail returns the last n lines of a file, for an error message.
func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(no " + path + ")"
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// AP starts the access point: WPA2, DHCP for the clients.
func (r Runner) AP() error {
	r.stopAll()
	if len(r.PSK) < 8 {
		return errors.New("access point key must be 8 or more characters")
	}
	conf := fmt.Sprintf("interface=%s\ndriver=nl80211\nssid=%s\nhw_mode=g\nchannel=6\nieee80211n=1\nwpa=2\nwpa_key_mgmt=WPA-PSK\nrsn_pairwise=CCMP\nwpa_passphrase=%s\n", r.Iface, r.SSID, r.PSK)
	if err := os.WriteFile(apConf, []byte(conf), 0o600); err != nil {
		return err
	}
	if _, err := r.busybox("ifconfig", r.Iface, "up"); err != nil {
		return fmt.Errorf("bring %s up: %w", r.Iface, err)
	}
	if out, err := r.out("hostapd", "-B", "-d", "-f", "/tmp/hostapd.log", "-P", "/tmp/hostapd.pid", apConf); err != nil {
		return fmt.Errorf("hostapd: %s", out)
	}
	if out, err := r.busybox("ifconfig", r.Iface, r.APAddr, "netmask", "255.255.255.0"); err != nil {
		return fmt.Errorf("address: %s", out)
	}
	base := r.APAddr[:strings.LastIndex(r.APAddr, ".")]
	if out, err := r.out("dnsmasq", "--interface="+r.Iface, "--bind-interfaces", "--except-interface=lo",
		"--dhcp-range="+base+".10,"+base+".50,12h", "--no-resolv", "--no-hosts", "--pid-file=/tmp/dnsmasq.pid", "--dhcp-leasefile=/tmp/dnsmasq.leases", "--user=root", "--group=root"); err != nil {
		return fmt.Errorf("dnsmasq: %s", out)
	}
	return nil
}

// station starts wpa_supplicant with no networks configured.
func (r Runner) station() error {
	if _, err := r.cli("status"); err == nil {
		return nil
	}
	r.stopAll()
	os.MkdirAll(ctlDir, 0o700)
	if err := os.WriteFile(staConf, []byte("ctrl_interface="+ctlDir+"\nupdate_config=0\n"), 0o600); err != nil {
		return err
	}
	if _, err := r.busybox("ifconfig", r.Iface, "up"); err != nil {
		return fmt.Errorf("bring %s up: %w", r.Iface, err)
	}
	// Not -B: this build has no log-file option, and a daemonised
	// supplicant's output is lost. Start it ourselves, output to its log.
	lf, err := os.OpenFile("/tmp/wpa_supplicant.log", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	c := r.cmd("wpa_supplicant", "-d", "-i", r.Iface, "-c", staConf, "-Dnl80211")
	c.Stdout, c.Stderr = lf, lf
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logTool("wpa_supplicant", c.Args[1:], "", nil)
	if err := c.Start(); err != nil {
		return fmt.Errorf("wpa_supplicant: %v", err)
	}
	os.WriteFile("/tmp/wpa.pid", []byte(strconv.Itoa(c.Process.Pid)), 0o600)
	go c.Wait()
	for i := 0; i < 20; i++ {
		if _, err := r.cli("status"); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("wpa_supplicant did not start (running: %v): %s", alive("/tmp/wpa.pid"), tail("/tmp/wpa_supplicant.log", 4))
}

// Scan lists the networks in range, strongest first, stopping the access
// point to do so.
func (r Runner) Scan() ([]Network, error) {
	if err := r.station(); err != nil {
		return nil, err
	}
	if out, err := r.cli("scan"); err != nil && !strings.Contains(out, "OK") {
		return nil, fmt.Errorf("scan: %s", out)
	}
	time.Sleep(4 * time.Second)
	out, err := r.cli("scan_results")
	if err != nil {
		return nil, err
	}
	return ParseScan(out), nil
}

// ParseScan reads wpa_cli's scan_results: bssid, frequency, signal, flags,
// ssid, tab-separated under a header. Hidden networks (no SSID) are left
// out; of one SSID on several access points the strongest stays.
func ParseScan(out string) []Network {
	best := map[string]Network{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 5 || f[0] == "bssid / frequency / signal level / flags / ssid" || strings.HasPrefix(f[0], "bssid") {
			continue
		}
		ssid := f[4]
		if ssid == "" || strings.Contains(ssid, "\\x00") {
			continue
		}
		sig, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		n := Network{SSID: ssid, Signal: sig, Secured: strings.Contains(f[3], "WPA") || strings.Contains(f[3], "WEP")}
		if old, ok := best[ssid]; !ok || sig > old.Signal {
			best[ssid] = n
		}
	}
	res := make([]Network, 0, len(best))
	for _, n := range best {
		res = append(res, n)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Signal > res[j].Signal })
	return res
}

func quote(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }

// Join connects to ssid (psk empty for an open network) and takes an
// address by DHCP.
func (r Runner) Join(ssid, psk string) error {
	if ssid == "" || strings.ContainsAny(ssid, "\n\r\x00") || strings.ContainsAny(psk, "\n\r\x00") {
		return errors.New("bad network name or key")
	}
	if psk != "" && (len(psk) < 8 || len(psk) > 63) {
		return errors.New("the key is 8 to 63 characters")
	}
	if err := r.station(); err != nil {
		return err
	}
	r.busybox("ifconfig", r.Iface, "0.0.0.0")
	kill("/tmp/udhcpc.pid")
	r.cli("remove_network", "all")
	id, err := r.cli("add_network")
	if err != nil {
		return fmt.Errorf("add_network: %s", id)
	}
	set := func(k, v string) error {
		if out, err := r.cli("set_network", id, k, v); err != nil || out != "OK" {
			return fmt.Errorf("set %s: %s", k, out)
		}
		return nil
	}
	if err := set("ssid", quote(ssid)); err != nil {
		return err
	}
	if psk == "" {
		err = set("key_mgmt", "NONE")
	} else {
		err = set("psk", quote(psk))
	}
	if err != nil {
		return err
	}
	if out, err := r.cli("select_network", id); err != nil || out != "OK" {
		return fmt.Errorf("select_network: %s", out)
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		st := r.wpaStatus()
		if st["wpa_state"] == "COMPLETED" {
			break
		}
		if time.Now().After(deadline) {
			r.cli("remove_network", "all")
			if psk != "" {
				return errors.New("could not connect: wrong password or out of range")
			}
			return errors.New("could not connect")
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := os.WriteFile(script, []byte(r.Dir+"/bin/busybox ifconfig \"$interface\" \"$ip\" netmask \"${subnet:-255.255.255.0}\"\n"+
		"[ -n \"$router\" ] && "+r.Dir+"/bin/busybox route add default gw \"${router%% *}\" dev \"$interface\"\n"+
		"[ -n \"$dns\" ] && : > /etc/resolv.conf && for d in $dns; do echo \"nameserver $d\" >> /etc/resolv.conf; done\n"+
		"exit 0\n"), 0o755); err != nil {
		return err
	}
	// The script's first line is a shebang for busybox's own ash.
	b, _ := os.ReadFile(script)
	os.WriteFile(script, append([]byte("#!"+r.Dir+"/bin/busybox ash\n"), b...), 0o755)
	if out, err := r.busybox("udhcpc", "-i", r.Iface, "-s", script, "-p", "/tmp/udhcpc.pid", "-n", "-q", "-t", "5", "-T", "3"); err != nil {
		return fmt.Errorf("no address from the network (DHCP): %s", out)
	}
	return nil
}

func (r Runner) wpaStatus() map[string]string {
	m := map[string]string{}
	out, err := r.cli("status")
	if err != nil {
		return m
	}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m
}

func (r Runner) addr() string {
	ifc, err := net.InterfaceByName(r.Iface)
	if err != nil {
		return ""
	}
	as, _ := ifc.Addrs()
	for _, a := range as {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	return ""
}

func alive(pidfile string) bool {
	b, err := os.ReadFile(pidfile)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && syscall.Kill(pid, 0) == nil
}

func (r Runner) Status() Status {
	if !r.Available() {
		return Status{Mode: "none", State: r.Iface + " not found: the driver or its firmware is not loaded (see the Log)"}
	}
	switch {
	case alive("/tmp/hostapd.pid"):
		return Status{Mode: "ap", SSID: r.SSID, IP: r.addr(), State: "AP"}
	case alive("/tmp/wpa.pid"):
		st := r.wpaStatus()
		return Status{Mode: "client", SSID: st["ssid"], IP: r.addr(), State: st["wpa_state"]}
	}
	return Status{Mode: "off"}
}

// Off stops everything and leaves the radio idle.
func (r Runner) Off() { r.stopAll() }
