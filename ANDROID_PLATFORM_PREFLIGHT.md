# Android platform preflight

Status: emulator MVP authorized. Physical-phone/OEM/distribution evidence remains a later rollout gate.

## Verified emulator/toolchain evidence

- Host SDK: `~/Library/Android/sdk`
- Platforms: Android 35 and 36
- Build tools: 34.0.0, 35.0.1, 36.0.0
- System image: `system-images;android-35;google_apis;arm64-v8a`
- Existing AVD: `call_blocker_api35`, Pixel 8, API 35 Google APIs arm64-v8a
- JDK: Homebrew OpenJDK 17.0.19 at `/opt/homebrew/opt/openjdk@17`
- With `JAVA_HOME=/opt/homebrew/opt/openjdk@17`: `sdkmanager --version` reports 20.0 and `emulator -version` reports 36.5.11.0.

The toolchain is reproducibly usable only when `JAVA_HOME` and `PATH` select that JDK. The emulator MVP uses product name `Mecha` and application ID `life.meyers.mecha`. Physical distribution and foreground-service policy remain separately gated.

## Physical-device evidence

Tailscale identifies the target as `note.tailcdfd89.ts.net` (`100.100.214.15`), a Galaxy Note20 Ultra 5G running Android, with Tailscale 1.98.5 observed on the host. This is network metadata only. It does not establish the phone Android/API/build number, OEM battery policy, phone Tailscale version, or actual foreground-service behavior.

`adb devices -l` has no attached device. A host-side `tailscale ping note.tailcdfd89.ts.net` reached the phone through DERP but did not establish a direct connection; it establishes only Tailnet reachability, not device facts. Do not infer device facts from Tailnet status.

## Required before physical rollout

1. Record the phone Android version/API/build, manufacturer power policy, and installed Tailscale version from the phone itself.
2. Identify a truthful foreground-service type and permissions for the selected target SDK and direct-APK distribution path, with primary Android policy references.
3. Run the visible-activity foreground-service harness through the required Home, screen-off, Doze, Tailscale, kill, force-stop, and reboot observations.
4. Record an explicit physical-rollout go/no-go decision.

Tailnet ACL application, Serve configuration, and second-node denial testing remain part of the later physical-phone gate.
