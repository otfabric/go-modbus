# spec/

`MODBUS-SPEC.md` is a Markdown conversion of the MODBUS Application Protocol Specification, kept here for reference while reading the code. No code or test reads it, and it is not part of the Go module's API. The publisher's PDF is the authoritative text: where the two differ, the PDF is right.

## Provenance

| | |
|---|---|
| Document | MODBUS Application Protocol Specification V1.1b3, dated April 26, 2012, published by the Modbus Organization |
| Source | <https://www.modbus.org/file/secure/modbusprotocolspecification.pdf> |
| Source as checked on 2026-11-14 | 50 pages, 932,519 bytes, SHA-256 `f80d0d7132fd035104c3f21b01026470e3310ca3d8d493d9a9737d0d32ad2eb1` |
| This copy | `MODBUS-SPEC.md`, 88,413 bytes, SHA-256 `5b7bc34e61dd0ebcbbb1f33c97b3808b5e97eb6123b627a155d6a45cf5c5e255`. In the repository since the initial commit of 2026-03-12. The date it was retrieved and the tool that converted it were not recorded |

To check the copy: `shasum -a 256 spec/MODBUS-SPEC.md`.

## Differences from the source

The copy was compared with the text of the PDF on 2026-11-14. 99.4 % of its words are in the PDF; the rest are artefacts of the conversion:

- The page headers and footers are dropped. The edition is named there, so the title of the copy reads "V1.1b" without the 3. The text is that of V1.1b3.
- The converter split some words ("w rite", "gatew ay", "protoc ol") and reflowed the tables and figures. Figures are not reproduced.

Clause numbers cited in the code and in the documents of this repository refer to V1.1b3.

## Terms

- The PDF states no copyright notice and no licence terms. It says that MODBUS is a registered trademark of Schneider Automation Inc.
- The Modbus Organization offers the document for download at <https://www.modbus.org/modbus-specifications>.
- The text belongs to its publisher. The MIT licence of this repository covers the repository's own code and documents, not this file.
- The copy is kept by the maintainer's decision of 2026-11-14. If the publisher states terms that do not allow it, the file is replaced by a note that says where to download the document and how to verify it.
