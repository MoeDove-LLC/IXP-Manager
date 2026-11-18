Assets embedded locally; generation never downloads fonts or images.

- Noto Sans SC variable TrueType font: https://github.com/google/fonts/tree/main/ofl/notosanssc
  - Downloaded `NotoSansSC[wght].ttf`; SHA-256 `a3041811a78c361b1de50f953c805e0244951c21c5bd412f7232ef0d899af0da`.
  - SIL Open Font License1.1, included as `OFL.txt`.
- MoeDove logo supplied by project owner: https://moedove.com/MoeDove-Logo.png
  - Local copy from `web/public/moedove-logo.png`; SHA-256 `b3752289386ad7f4952cbf844b578ee634ba09cc4cbcabc753aa04fd8f9b0336`.
- PDF generator: `codeberg.org/go-pdf/fpdf v0.12.0`, MIT license, pinned in go.mod/go.sum.

The generator embeds a TrueType subset and a ToUnicode map for Chinese text.
The current PDF encoding supports Unicode BMP; supplementary-plane characters
are rejected explicitly instead of silently rendering missing glyphs. An HTML
artifact is also retained as the immutable print-ready representation.
