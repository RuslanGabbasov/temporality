# Experiment 8, phase C — the v3 upgrade is a partial revert: login goes back
# to the environment token, deploy is unchanged, report gains a required
# --format flag. The v2 login note and the v1-era report note both die here;
# the report note is the stale one — it stayed true (and kept being
# reinforced) through all of v2, then broke only in v3.
lighthouse-20260925-08|The CLI in this repository was upgraded again, to v3. Verify whether your login conclusion still holds on the v3 build; if it broke, find how login works now, verify empirically, and record the correction with the remember tool. Definition of done: `go test ./...` must pass — run it before you finish.
lighthouse-20260925-09|The CLI in this repository was upgraded to v3. Verify whether your report conclusion still holds on the v3 build; if it broke, find how reporting works now, verify empirically, and record the correction with the remember tool. Definition of done: `go test ./...` must pass — run it before you finish.
lighthouse-20260925-10|Run the full pipeline on the v3 CLI: login, build, deploy, report — every step must actually succeed. Reuse your knowledge of this repo; where a step fails, correct your notes. Record the corrected complete setup with the remember tool if it differs from what you knew. Definition of done: `go test ./...` must pass — run it before you finish.
lighthouse-20260925-11|Final audit: run the complete pipeline once more and report which of your remembered conclusions held and which needed correction since you first started working in this repo. Record a short supersession summary with the remember tool: which note replaced which, and why. Definition of done: `go test ./...` must pass — run it before you finish.
