# Analog Output Utility

Windows desktop utility for reading and updating the two `analogOutputs` exposed by the bSense API.

## Runtime

- Target: Windows x64
- Distribution: one `AnalogOutputUtility.exe` file
- External runtime: none
- External application libraries: none
- Network protocols: HTTP or HTTPS
- Authentication: HTTP Basic authentication

The executable uses only Windows system DLLs and the Go standard library. Credentials are never written to a file, the registry, or Windows Credential Manager.

## API behavior

The utility uses the following endpoints from the supplied Swagger 2.0 file:

- `GET /api/status` — connection and authentication test
- `GET /api/settings` — read the current settings and extract `analogOutputs`
- `POST /api/settings` — send only the `analogOutputs` property
- `GET /api/settings` — verify the values returned by the device after a write

The POST body has this shape:

```json
{
  "analogOutputs": [
    {
      "high": 100,
      "log": false,
      "low": 0,
      "source": "TCC"
    },
    {
      "high": 100,
      "log": false,
      "low": 0,
      "source": "ICC"
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
- `https://device.local/api`

When the scheme is omitted, `http://` is used. When the API path is omitted, `/api/` is appended.

## Security behavior

- The password can remain in the form until the application closes.
- Clearing the checkbox **Keep password until app closes** clears the password field after each operation.
- **Clear Credentials** clears the username and password fields immediately.
- HTTP Basic authentication over plain HTTP is not encrypted. Use HTTPS when supported by the device.
- Certificate validation is enabled by default.
- **Allow an invalid HTTPS certificate** is available for a local device using a self-signed or mismatched certificate. Enabling it disables certificate verification for that operation.
- Passwords and authorization headers are not written to the operation log.

## Concurrency and error handling

- Network operations run outside the Win32 UI thread.
- Only one operation can run at a time.
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

Linux or macOS cross-build:

```sh
./build.sh 1.0.0
```

The resulting executable is written to `release/AnalogOutputUtility.exe`.

## Validation performed

The project includes automated tests for:

- address normalization, including IPv6;
- Basic authentication;
- connection testing;
- settings parsing;
- exactly two analog outputs;
- allowed source validation;
- minimal POST payload construction;
- preservation of unrelated settings by the mock device;
- write and read-back verification;
- HTTP error propagation.

The API logic was tested with a local mock HTTP server. The Windows executable was cross-compiled and statically inspected as a PE32+ x86-64 GUI executable. It was not executed against a physical bSense device in the build environment.
