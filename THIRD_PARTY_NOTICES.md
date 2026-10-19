# Third-party notices

Release archives include the corresponding license texts under `LICENSES/`.

## Compiled dependencies

| Modules | License |
| --- | --- |
| `github.com/spf13/cobra`, `github.com/inconshreveable/mousetrap` | Apache-2.0 |
| `golang.org/x/crypto`, `golang.org/x/mod`, `golang.org/x/sys`, `golang.org/x/term`, `github.com/spf13/pflag` | BSD-3-Clause |

`github.com/spf13/pflag` is Copyright (c) 2012 Alex Ogier and the Go
Authors. The `golang.org/x/*` modules are Copyright 2009 The Go Authors.

## Design reference

The architecture of `codex-switch` was informed by the MIT-licensed CC Switch
project:

- Project: https://github.com/farion1231/cc-switch
- Copyright: CC Switch contributors
- License: MIT

No CC Switch user interface, provider catalog, routing implementation, or OAuth
client implementation is included. If source is ported in the future, its exact
copyright and license notice must be added here before release.
