// The stage has no font files, so the fonts (fonts/, DejaVu) are embedded
// and rasterised at run time: at whatever scale the panel calls for, and
// with every glyph they have, not just the UI's own strings.
fn main() {
    let config = slint_build::CompilerConfiguration::new()
        .with_include_paths(vec!["fonts".into()])
        .embed_resources(slint_build::EmbedResourcesKind::EmbedFiles);
    slint_build::compile_with_config("ui/app.slint", config).unwrap();
}
