// The stage has no font files, so the fonts (fonts/, DejaVu) are embedded
// and rasterised at run time: at whatever scale the panel calls for, and
// with every glyph they have, not just the UI's own strings.
fn main() {
    let config = slint_build::CompilerConfiguration::new()
        .with_include_paths(vec!["fonts".into()])
        .embed_resources(slint_build::EmbedResourcesKind::EmbedFiles);
    slint_build::compile_with_config("ui/app.slint", config).unwrap();
    glow();
}

// The backdrop's glow (ui/app.slint), as raw RGBA for src/app.rs: white,
// coloured by `colorize`, so drawing it is an image blit rather than a
// gradient per pixel. Raw, not PNG: the fbdev build has no image decoder.
// The falloff is what the radial gradient did: alpha 0.3 at the centre,
// linear to 0 at 68% of the circle that reaches the corners.
const GLOW_SIZE: usize = 256;

fn glow() {
    let n = GLOW_SIZE as f32;
    let end = 0.68 * n * std::f32::consts::FRAC_1_SQRT_2;
    let mut px = Vec::with_capacity(GLOW_SIZE * GLOW_SIZE * 4);
    for y in 0..GLOW_SIZE {
        for x in 0..GLOW_SIZE {
            let r = (x as f32 + 0.5 - n / 2.0).hypot(y as f32 + 0.5 - n / 2.0);
            let a = 0.3 * (1.0 - r / end).max(0.0);
            px.extend_from_slice(&[255, 255, 255, (a * 255.0).round() as u8]);
        }
    }
    let out = std::path::PathBuf::from(std::env::var("OUT_DIR").unwrap());
    std::fs::write(out.join("glow.rgba"), px).unwrap();
    std::fs::write(out.join("glow.rs"), format!("pub const GLOW_SIZE: u32 = {GLOW_SIZE};\n")).unwrap();
}
