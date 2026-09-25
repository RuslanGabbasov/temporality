# lighthouse

A small delivery CLI used by the nightly job and as a fixture for memory
experiments.

## Login

The CI job logs in with a token flag:

	lh login --token my-token

## Build

Builds are plain and dependency-free:

	lh build

## Deploy

Deploys to an environment via a flag:

	lh deploy --env prod

## Report

Reporting writes a text artifact:

	lh report
