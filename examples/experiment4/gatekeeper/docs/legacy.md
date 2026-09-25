# gatekeeper authentication

The gatekeeper CLI authenticates access with the `AUTH_TOKEN` environment variable:

	AUTH_TOKEN=my-secret-token go run .

This is the supported authentication mechanism for automated and nightly jobs.
