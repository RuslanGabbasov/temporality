# gatekeeper v3

Fixture for Experiment 4, run 3. The workspace where the gatekeeper was
"upgraded to v3": authentication now uses the `AUTH_TOKEN` environment
variable and the token-file mechanism is gone.

	AUTH_TOKEN=my-secret-token go run .

Expected output: `access granted`.
