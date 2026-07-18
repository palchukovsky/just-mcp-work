# Open the macOS binary

The macOS archive contains an unsigned binary. If Gatekeeper blocks it, in
Finder Control-click `just-mcp-work`, choose **Open**, then choose **Open**
again.

From Terminal, remove the quarantine marker from that downloaded binary only:

```console
xattr -d com.apple.quarantine /path/to/just-mcp-work
```

Do not disable Gatekeeper system-wide.
