# Windows ZCode 3.14 snapshot contract review

Reviewed on 2026-09-20 using installed **public program resources only**. No client
settings, sessions, checkpoint data, credentials, or user manifests were read; no
client task or upload was triggered. This is static producer verification, not a
live upload test.

| Public program | Identity |
| --- | --- |
| `ZCode.exe` PE file version | `3.14.0.7681` |
| `ZCode.exe` SHA-256 | `564db1fd7b7c9cb71deca8178f7ffd865ace870fb0240e02c93a3300499118be` |
| `resources/app.asar` SHA-256 | `8604b5f47b0f4bf9e900901d8c60a0dcf6406b89879da872640ae27026b628cb` |
| `resources/glm/zcode.cjs` SHA-256 | `8f5cfccf2a899b92e57bc2a5760b949c1a928f739652fffc9e6d07c24f11ba05` |
| ASAR `out/host/index.js` SHA-256 | `64bef01c15ba60a8e142b0d5b2639588c7c8dfdce4f71e810c4fb08e33223d50` |
| ASAR `out/host/chunk-JET77OYC.js` SHA-256 | `27848561c3c5ebfea99f616bf3c9587fa14e0a7740de8696eeba8070a6f42905` |

`scripts/dev/inspect_zcode_asar.py` reads bounded source snippets without executing
or extracting the application. Character offsets below identify the reviewed
UTF-8 JavaScript; hashes make those offsets reproducible.

## Producer and runtime

In `out/host/index.js`, `isGitCheckpointMeta` starts at character 216404 and
`GitCheckpointStore` at 216756. Its accepted record fields are strings
`checkpointId`, `workspacePath`, `repoRoot`, `workspaceInRepoPath`, `refName`, and
`commitOid`, numeric `createdAt`, and literal `scope: "workspace"`. The producer
creates a Git tree/commit/ref and atomically writes one `<checkpointId>.json`
metadata record. This is local checkpoint metadata; it is not evidence of upload,
server acceptance, or remote retention.

The default store joins the application's v2 directory with `checkpoints` and
the first 12 hexadecimal characters of the workspace-key SHA-256. In the reviewed
`chunk-JET77OYC.js`, `getDataBaseDir` permits an application override,
`ZCODE_DATA_BASE_DIR`, and `HOME`/OS home; the root is not safely inferred from a
single fixed Windows path. These functions describe the producer; Laodi does not
open guessed directories based on this review.

`resolveDefaultZCodeAgentCommand` in the same chunk prefers the Electron node
runtime. `findZCodeAgentRuntimeNodeBundle` (character 119394) first selects
`process.resourcesPath/glm/zcode.cjs`, which exists in the reviewed installation.
The documented fallback `~/.zcode/server/agents/glm/zcode.cjs` was absent. The
entrypoint also permits explicit runtime/development overrides; such a runtime
needs a separate review.

Searching every packed ASAR `.js` member and the installed `app.asar.unpacked`
and `glm` `.js`/`.cjs` program files found no `repo_snapshot`,
`lastAcceptedManifestHash`, `nextExtraManifestHash`, or `extra-manifests` tokens.
Together with the identified producer, this rules out treating these reviewed
bytes as the old scanner's verified upload contract. It does not prove that no
other network feature or dynamically selected runtime can upload data.

## Scanner decision

`DetectBuild` now returns a Windows namespace with the PE version and a composite
SHA-256 over the executable, ASAR and bundled runtime hashes. It bounds reads and
rejects ADS, reparse ancestors and hardlinks. Replacing any of those three program
files invalidates the scanner's cached identity.

The reviewed build remains `unsupported_build`, with diagnostic
`windows_zcode_git_checkpoint_schema_unsupported`. No upload schema is accepted
for this Windows build. Changed or unknown program bytes receive
`zcode_build_not_verified`. Existing macOS `KnownBuild` behavior and the explicit
synthetic `--build` override remain available. Hook observation has its own
separately verified contract and does not acquire snapshot coverage from this
identity check.

The opt-in native identity test reads only public program resources:

```powershell
$env:LAODI_TEST_ZCODE_APP = 'C:\path\to\ZCode.exe'
go test ./internal/laodi -run TestWindowsInstalledSnapshotProgramIdentity -count=1
```
