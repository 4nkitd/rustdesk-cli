# Upstream compatibility

Source research checked on 2026-10-20. GitHub reported **1.5.0** as the latest stable release, published 2026-09-30.

- Release source: [`1.5.0`, `fada664df7a294d1d1a9ca3e7cd3637069122f17`][release-tree].
- Upstream master snapshot: [`53996e3c404ea5083df0775057e4149e49102084`][master-tree], 43 commits ahead of that release.
- [Release metadata][release-api] and [release-to-master comparison][comparison]. The API is mutable; the commits above identify the researched sources.
- The release protocol lives in [`libs/base/protos/message.proto`][protocol]; its pinned `hbb_common` dependency implements [Zstandard compression][compression].

## Source-verified matrix

These gates describe upstream implementations, not proof that rustdesk-cli interoperates with a particular installed binary. Version numbers alone do not establish permissions, platform capabilities or capture availability.

| Capability | Verified version gate | Platform and permission gates | Wire semantics |
| --- | --- | --- | --- |
| Screenshot | **1.4.0+**; absent in inspected 1.3.9 handler | Desktop and Android shared capture path; authorized remote-control or camera session and working display capture. File-transfer and terminal scopes reject it in 1.5.0. | `ScreenshotRequest.display` selects the display; response echoes `sid`. Success is **PNG bytes** with empty `msg`; errors use `msg`. [Dispatch][screenshot-dispatch], [capture and encoding][screenshot-encoding]. |
| Legacy clipboard text | Verified in **1.1.9** and 1.5.0; first-ever release not established | Desktop and Android. In 1.5.0, incoming text requires remote-control scope and clipboard enabled. Outgoing subscription also requires keyboard enabled and no one-way clipboard restriction. | `Clipboard.content` is UTF-8, optionally Zstandard compressed; text format is `0`. It is synchronization/write data, with **no write ACK**. [Legacy implementation][clipboard-legacy], [current conversion][clipboard-conversion], [permissions][clipboard-permissions], [scope][clipboard-scope]. |
| `MultiClipboards` | **1.3.0+ desktop; 1.3.3+ Android** | Upstream capability helper excludes empty platform and iOS. Same clipboard session/permission restrictions. | Modern text can arrive inside `MultiClipboards`; receivers must handle both envelopes. [Exact gates and legacy fallback][clipboard-gates]. |
| `Auth2FA.code` | **1.2.4+** implementation. GitHub marks 1.2.4 and 1.2.5 prereleases; 1.2.6 is the first subsequent non-prerelease verified. | Shared authentication implementation, including Android configuration support. | On login error `2FA Required`, send `Auth2FA.code` on the same connection. Wrong code returns `Wrong 2FA Code`; wait for successful `LoginResponse.PeerInfo`. [Initial implementation][auth-initial], [current verification][auth-current]. |
| Trusted-device `Auth2FA.hwid` | **1.3.0+**; inspected 1.2.x messages contain only `code` | Requires server trusted-device option; challenge advertises `enable_trusted_devices`. | Optional enrollment after successful TOTP. Basic 2FA does not require HWID. [Enrollment][auth-trusted]. |
| Persistent terminal and reattach | **1.4.1+** | Require **`PeerInfo.features.terminal`**, terminal permission and authentication. Desktop only. Windows also checks ConPTY availability; specified-user login requires installed RustDesk. | Set `terminal_persistent=Yes`; retain returned `service_id` and terminal IDs. Reconnect with `LoginRequest.terminal.service_id`, then `OpenTerminal` for the retained ID. [Capability][terminal-capability], [login gates][terminal-login], [initial reattach][terminal-initial]. |
| Terminal replay | **1.4.1–1.4.5:** replay unfinished. **1.4.6+:** replay and initial ID remapping. **1.4.7+:** `replay_terminal_output` flag. | Same terminal gates. | Replay is bounded and may lose output produced while disconnected. It is not lossless command-output resumption. [1.4.5][terminal-145], [1.4.6][terminal-146], [1.4.7][terminal-147], [1.5.0 replay][terminal-replay]. |
| `FileRename` | **1.3.0+**; absent in inspected tags through 1.2.7 | File-transfer session and permission; one-way policy can reject rename. Desktop and Android handlers; Android workspace restrictions apply in 1.5.0. | Destination is `parent(path).join(new_name)`. Match `FileResponse.done/error` by job ID, normally `file_num=0`. **No overwrite-confirmation handshake**; filesystem rename semantics determine replacement. [UI gate][rename-gate], [session/policy gates][rename-policy], [filesystem operation][rename-fs], [result mapping][rename-result]. |

iOS is not a supported controlled endpoint for these operations. Mobile controller support is a separate question from controlled-side support.

## Crucial implementation details

### Clipboard reads and writes

There is **no reliable snapshot “get clipboard now” operation** in this protocol. The 1.5.0 desktop service sends clipboard-change events and ignores Linux initial-selection events. Connecting and waiting does not guarantee existing contents will arrive. A timeout means “no update received,” not “clipboard empty.” [Service implementation][clipboard-service].

An empty `Clipboard` is not a read request. Current outgoing sync filters empty text, current incoming decoding passes valid empty text to the setter, and 1.1.9 avoids setting empty text. Portable clipboard clearing is therefore not established. A successful send is not confirmation that the remote clipboard changed. [Empty-text handling][clipboard-empty], [legacy behavior][clipboard-legacy].

### Screenshots

Use the **1.4.0** gate. Upstream's `is_support_screenshot_num()` checks it correctly, but `is_support_screenshot(&str)` incorrectly delegates to the 1.2.4 multi-UI-session gate. [Helpers][screenshot-gate].

Requests wait for a valid captured frame. Pending requests are keyed by source/display, so another request for the same source/display replaces the earlier one. Serialize requests and use a timeout; unavailable capture or an invalid display need not return an immediate error. [Pending storage and response generation][screenshot-encoding].

### Terminal reattachment

The feature flag is authoritative; `version >= 1.4.1` alone is insufficient. Windows uses a runtime ConPTY symbol check whose failure message requires Windows 10 October 2018 or newer. [Pinned dependency check][conpty].

**An absent service or terminal can create a replacement.** Server initialization creates a service when the supplied ID is unknown, and opening a missing terminal can create a shell. From 1.4.6, the first reconnect open may instead remap the lowest surviving terminal ID. `opened.success=true` alone does not prove that the intended old process was reattached. [Service creation][terminal-service], [open behavior][terminal-replay].

There is no separate wire list/attach action; `persistent_sessions` reports other IDs during initial synchronization. Persistence is an in-memory server-process registry, not restart/reboot durability. In 1.5.0, replay targets the latest 8 KiB, and the implementation explicitly documents disconnected-output loss. [Registry and limits][terminal-registry], [replay][terminal-replay].

The pinned master adds an oversized unterminated-line buffer fix that is **not shipped in 1.5.0**. Do not attribute that fix to the release. [Master buffer implementation][terminal-master].

### Rename behavior

Use a filename for portable `new_name` behavior. Version 1.5.0 rejects empty names, NUL, `..` components and platform-specific absolute paths; it does not categorically reject relative subdirectories. Existing-destination behavior follows `std::fs::rename`, including platform-specific overwrite behavior. Do not present it as a no-clobber operation. [Validation][rename-validation], [rename][rename-fs].

## Live verification still required

This research used public sources only. No installed binaries, remote sessions, user data or credentials were used. It does **not** establish live rustdesk-cli compatibility.

Outstanding checks include Android clipboard restrictions/capture/scoped storage; Linux X11, Wayland and DRM behavior; screenshot capture availability; rename collisions; and terminal reattachment across 1.4.1, 1.4.5, 1.4.6 and 1.4.7+ with missing services, noncontiguous IDs and replay loss.

[release-tree]: https://github.com/rustdesk/rustdesk/tree/fada664df7a294d1d1a9ca3e7cd3637069122f17
[master-tree]: https://github.com/rustdesk/rustdesk/tree/53996e3c404ea5083df0775057e4149e49102084
[release-api]: https://api.github.com/repos/rustdesk/rustdesk/releases/latest
[comparison]: https://github.com/rustdesk/rustdesk/compare/fada664df7a294d1d1a9ca3e7cd3637069122f17...53996e3c404ea5083df0775057e4149e49102084
[protocol]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/libs/base/protos/message.proto
[compression]: https://github.com/rustdesk/hbb_common/blob/229b904508364c8997aad0fb5af57effac859f60/src/compress.rs#L17-L53
[screenshot-dispatch]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/connection.rs#L4043-L4052
[screenshot-encoding]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/video_service.rs#L1455-L1527
[screenshot-gate]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/common.rs#L190-L198
[clipboard-legacy]: https://github.com/rustdesk/rustdesk/blob/1.1.9/src/common.rs#L43-L94
[clipboard-conversion]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/clipboard.rs#L640-L750
[clipboard-permissions]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/connection.rs#L2311-L2324
[clipboard-scope]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/connection.rs#L5737-L5739
[clipboard-gates]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/clipboard.rs#L543-L595
[clipboard-service]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/clipboard_service.rs#L69-L108
[clipboard-empty]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/clipboard.rs#L400-L427
[auth-initial]: https://github.com/rustdesk/rustdesk/blob/1.2.4/src/server/connection.rs#L1753-L1768
[auth-current]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/connection.rs#L3045-L3084
[auth-trusted]: https://github.com/rustdesk/rustdesk/blob/1.3.0/src/server/connection.rs#L1868-L1875
[terminal-capability]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/connection.rs#L2065-L2079
[terminal-login]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/connection.rs#L2897-L2916
[terminal-initial]: https://github.com/rustdesk/rustdesk/blob/1.4.1/src/server/terminal_service.rs#L707-L747
[terminal-145]: https://github.com/rustdesk/rustdesk/blob/1.4.5/src/server/terminal_service.rs#L780-L815
[terminal-146]: https://github.com/rustdesk/rustdesk/blob/1.4.6/src/server/terminal_service.rs#L903-L990
[terminal-147]: https://github.com/rustdesk/rustdesk/blob/1.4.7/src/server/terminal_service.rs#L1074-L1143
[terminal-replay]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/terminal_service.rs#L1041-L1143
[terminal-service]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/terminal_service.rs#L161-L198
[terminal-registry]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/terminal_service.rs#L30-L105
[terminal-master]: https://github.com/rustdesk/rustdesk/blob/53996e3c404ea5083df0775057e4149e49102084/src/server/terminal_service.rs#L450-L520
[conpty]: https://github.com/rustdesk-org/wezterm/blob/80174f8009f41565f0fa8c66dab90d4f9211ae16/pty/src/win/psuedocon.rs#L36-L75
[rename-gate]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/flutter/lib/desktop/pages/file_manager_page.dart#L1148-L1158
[rename-policy]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/server/connection.rs#L3462-L3510
[rename-fs]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/libs/base/src/fs.rs#L1421-L1439
[rename-result]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/src/ui_cm_interface.rs#L1621-L1671
[rename-validation]: https://github.com/rustdesk/rustdesk/blob/fada664df7a294d1d1a9ca3e7cd3637069122f17/libs/base/src/fs.rs#L463-L490
