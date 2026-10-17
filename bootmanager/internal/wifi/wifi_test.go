package wifi

import "testing"

func TestParseScan(t *testing.T) {
	out := "bssid / frequency / signal level / flags / ssid\n" +
		"aa:bb:cc:00:00:01\t2437\t-60\t[WPA2-PSK-CCMP][ESS]\thome\n" +
		"aa:bb:cc:00:00:02\t2462\t-40\t[WPA2-PSK-CCMP][ESS]\thome\n" +
		"aa:bb:cc:00:00:03\t2412\t-70\t[ESS]\tcafe\n" +
		"aa:bb:cc:00:00:04\t2412\t-50\t[WPA2-PSK-CCMP][ESS]\t\n"
	got := ParseScan(out)
	if len(got) != 2 || got[0].SSID != "home" || got[0].Signal != -40 || !got[0].Secured || got[1].SSID != "cafe" || got[1].Secured {
		t.Fatalf("%+v", got)
	}
}
