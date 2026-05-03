package main

// Bindings is the Go-side object whose exported methods Wails surfaces to
// the Svelte frontend (auto-generated TypeScript wrappers under
// frontend/wailsjs/). Real methods land in tasks 62 and 63 (GetConfig /
// SaveConfig); this scaffold ensures the bindings codegen has a non-empty
// type to walk at build time.
type Bindings struct{}

// Greet is a Wails-default placeholder so the codegen produces at least one
// exported method until task 62 replaces it with GetConfig.
func (b *Bindings) Greet(name string) string { return "Hello, " + name }
