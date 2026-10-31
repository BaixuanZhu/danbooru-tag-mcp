module danbooru-tag-mcp

go 1.26

// Exact toolchain pin: patch releases of gc change emitted bytes, so
// reproducible builds (see README) require the exact same compiler. setup-go
// reads this file via go-version-file; local builds auto-download it
// (GOTOOLCHAIN=auto default). Bump deliberately, together with CI.
toolchain go1.26.5

require github.com/mark3labs/mcp-go v1.1.1 // indirect
