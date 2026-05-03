cask "noo-noo" do
  version "__VERSION__"
  sha256 "__SHA256_DMG__"

  url "https://github.com/FRIKKern/noo-noo/releases/download/v#{version}/Noo-Noo-v#{version}.dmg"
  name "Noo-Noo"
  desc "Smart cleanup for Mac developers (menubar app + CLI)"
  homepage "https://github.com/FRIKKern/noo-noo"

  depends_on macos: ":big_sur"

  app "Noo-Noo.app"
  binary "#{appdir}/Noo-Noo.app/Contents/Resources/bin/noo-noo", target: "noo-noo"
  binary "#{appdir}/Noo-Noo.app/Contents/Resources/bin/noo-nood", target: "noo-nood"

  postflight do
    system_command "#{appdir}/Noo-Noo.app/Contents/Resources/bin/noo-noo",
                   args: ["install"],
                   sudo: false
  end

  uninstall launchctl: "io.noo-noo.d",
            delete:    [
              "~/Library/LaunchAgents/io.noo-noo.d.plist",
              "~/Library/Application Support/noo-noo",
            ]

  zap trash: [
    "~/.config/noo-noo",
    "~/Library/Logs/noo-noo",
    "~/Library/Caches/noo-noo",
  ]

  caveats <<~EOS
    Noo-Noo is ad-hoc signed (no Apple Developer ID yet). The first time
    you launch the app, macOS Gatekeeper will block it. To allow it:

      1. Right-click /Applications/Noo-Noo.app and choose "Open"
      2. Click "Open" in the confirmation dialog

    macOS will remember this choice. Apple Developer ID signing &
    notarization land in Phase 0.4.1.
  EOS
end
