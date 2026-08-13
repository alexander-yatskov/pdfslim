# Third-party notices

## CCITT Group 4 encoder

Files in \`internal/ccittg4\` are adapted from the CCITT fax encoder in
[xwc1125/gopdf](https://github.com/xwc1125/gopdf), revision
\`fb7356931e4b\`. The original work is available under Apache License 2.0.
The license text is in \`internal/ccittg4/LICENSE\`.

Changes: the package name was changed and concurrent calls were serialized
because the original encoder stores active pixel polarity in package state.
