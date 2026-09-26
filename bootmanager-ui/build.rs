// The phone build (feature fbdev) has no font files and no fontconfig, so
// the glyphs are rendered at build time into the binary, at the scale factor
// the phone runs at. Desktop and web builds embed the font files instead and
// render text at run time.
//
// FONT_DIR is where DejaVuSans.ttf and DejaVuSans-Bold.ttf are found
// (Alpine: apk add font-dejavu).
fn main() {
    let font_dir = std::env::var("FONT_DIR").unwrap_or_else(|_| "/usr/share/fonts/dejavu".into());
    println!("cargo:rerun-if-env-changed=FONT_DIR");
    let config = slint_build::CompilerConfiguration::new()
        .with_include_paths(vec![font_dir.into()]);
    let config = if std::env::var_os("CARGO_FEATURE_FBDEV").is_some() {
        config
            .embed_resources(slint_build::EmbedResourcesKind::EmbedForSoftwareRenderer)
            .with_scale_factor(2.0)
    } else {
        config.embed_resources(slint_build::EmbedResourcesKind::EmbedFiles)
    };
    slint_build::compile_with_config("ui/app.slint", config).unwrap();
}
