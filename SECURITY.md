# Security policy

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Report them privately through GitHub's private vulnerability reporting: open the repository's **Security** tab and choose **Report a vulnerability**. If that option is not available, send a private message to the maintainer, @atiasadir on GitHub, asking for a private channel.

Please include what you found, how to reproduce it, and the version (`kubeshrink version`). You can expect an acknowledgement within a week.

## Scope

KubeShrink is designed to be read-only. Anything that lets it modify a cluster, read Secrets or ConfigMap data, send cluster data off the machine, or write exports with broad permissions is a security bug.

## Supported versions

The project is pre-1.0; only the latest commit on `main` receives fixes.
