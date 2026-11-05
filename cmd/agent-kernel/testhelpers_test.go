package main

// storedAudit is the shared capture shape of RecordAccessAudit fakes across
// the cmd test suite (the artifact lifecycle, auth and research-session
// tests record audit rows the same way).

type storedAudit struct {
	actor      string
	action     string
	entityKind string
	entityID   string
	details    map[string]any
}
