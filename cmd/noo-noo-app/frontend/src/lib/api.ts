// Mirrors internal/config.Config from the Go side. Keep field names in sync.
export interface Config {
  daemon: { scan_hour: number; socket_path: string; store_path: string }
  heuristics: {
    idle_repos: { enabled: boolean; min_idle_days: number; min_node_modules_bytes: number }
    cache_velocity: { enabled: boolean; growth_multiplier: number; window_days: number }
  }
  notify: { enabled: boolean; min_severity: 'low' | 'medium' | 'high' }
  scan: { roots: string[] }
}

// Wails generates window.go.main.Bindings.* at build time. We declare a
// minimal shim here so TypeScript compiles before codegen runs.
declare global {
  interface Window {
    go?: { main: { Bindings: {
      GetConfig(): Promise<Config>
      SaveConfig(c: Config): Promise<void>
      OpenConfigInEditor(): Promise<void>
    } } }
  }
}

const fallback: Config = {
  daemon: { scan_hour: 3, socket_path: '', store_path: '' },
  heuristics: {
    idle_repos: { enabled: true, min_idle_days: 30, min_node_modules_bytes: 524288000 },
    cache_velocity: { enabled: true, growth_multiplier: 2.0, window_days: 7 },
  },
  notify: { enabled: true, min_severity: 'medium' },
  scan: { roots: [] },
}

export async function loadConfig(): Promise<Config> {
  return window.go ? window.go.main.Bindings.GetConfig() : structuredClone(fallback)
}

export async function saveConfig(c: Config): Promise<void> {
  if (window.go) await window.go.main.Bindings.SaveConfig(c)
}

export async function openInEditor(): Promise<void> {
  if (window.go) await window.go.main.Bindings.OpenConfigInEditor()
}
