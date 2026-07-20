# Analog Output Utility

Windows and macOS utility for reading and updating the two `analogOutputs` exposed by the bSense API.

## Runtime

- Targets: Windows x64, macOS Apple Silicon, and macOS Intel
- Distribution: one executable per target
- External runtime: none beyond the operating system and the default browser on macOS
- External application libraries: none
- Network protocols: HTTP or HTTPS
- Authentication: HTTP Basic authentication

The Windows executable uses only Windows system DLLs and the Go standard library. The macOS executable uses the Go standard library and opens its interface in the default browser on a token-protected loopback address. The utility never writes credentials to disk.

## API behavior

The utility uses the following endpoints from the supplied Swagger 2.0 file:

- `GET /api/status` — connection and authentication test performed by **Connect**
- `GET /api/settings` — load the current `analogOutputs` during **Connect**
- `POST /api/settings` — send only the `analogOutputs` property
- `GET /api/settings` — verify the values returned by the instrument after a write

The POST body has this shape:

```json
{
  "analogOutputs": [
    {
      "high": 100000,
      "log": false,
      "low": 0,
      "source": "TCC"
    },
    {
      "high": 100,
      "log": false,
      "low": 0,
      "source": "HNAP"
    }
  ]
}
```

Only the following sources are accepted:

- `TCC`
- `ICC`
- `HNAP`
- `HNAC`
- `LNAC`

Exactly two outputs are required.

## Address input

Accepted examples:

- `192.168.1.50`
- `192.168.1.50:8080`
- `http://192.168.1.50`
- `https://instrument.local/api`

When the scheme is omitted, `http://` is used. When the API path is omitted, `/api/` is appended.

## Security behavior

- The password can remain in the form until the application closes.
- Clearing the checkbox **Keep password until app closes** clears the password field after each operation.
- **Clear Credentials** clears the username and password fields immediately.
- HTTP Basic authentication over plain HTTP is not encrypted. Use HTTPS when supported by the instrument.
- Certificate validation is disabled by default for local instruments using a self-signed or mismatched certificate.
- Clear **Allow an invalid HTTPS certificate** to enable certificate verification for an operation.
- Passwords and authorization headers are not written to the operation log.

## Concurrency and error handling

- Network operations do not block the interface.
- Only one operation can run at a time.
- **Send Settings** remains disabled until **Connect** has successfully tested the connection and loaded the current settings.
- Changing connection details disables **Send Settings** until the next successful **Connect**.
- The active operation can be cancelled.
- Every request has a configurable timeout from 1 to 300 seconds.
- HTTP 400, 401, 403, 404, 405, and 5xx responses receive specific messages.
- Connection refusal, DNS failure, routing failure, timeout, HTTP/HTTPS mismatch, and certificate errors receive specific messages.
- Response bodies are capped at 16 MiB.
- Writes are followed by a read-back comparison of all four properties on both outputs.

## Build from source

Requirements for building only:

- Go 1.23 or newer

PowerShell:

```powershell
./build.ps1 -Version 1.0.0
```

Linux or macOS build:

```sh
./build.sh 1.0.0
```

The resulting executables are:

- `release/AnalogOutputUtility.exe`
- `release/AnalogOutputUtility-macos-arm64` for Apple Silicon
- `release/AnalogOutputUtility-macos-amd64` for Intel Macs

On macOS, run the matching executable. It starts a loopback-only server and opens the utility in the default browser. Use **Quit Utility** in the page to stop it.

## Create a GitHub release

Open **Actions**, choose **Release downloads**, click **Run workflow**, and enter a version such as `1.1.0`. The workflow tests and builds the project, creates the matching `v1.1.0` tag and GitHub Release, and adds the Windows `.exe` and macOS executables as downloads.

Pushing a tag such as `v1.1.0` runs the same release workflow.

## Validation performed

The project includes automated tests for:

- address normalization, including IPv6;
- Basic authentication;
- connection testing;
- settings parsing;
- exactly two analog outputs;
- allowed source validation;
- minimal POST payload construction;
- preservation of unrelated settings by the mock instrument;
- write and read-back verification;
- HTTP error propagation.

The API logic was tested with a local mock HTTP server. Windows and macOS executables are cross-compiled during the shell build. They were not executed against a physical bSense instrument in the build environment.
