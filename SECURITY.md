# Security

For a sensitive vulnerability, contact the maintainer through GitHub's private
vulnerability reporting feature when enabled. Do not post credentials, real
miner exports, logs or database/key files in an issue. If private reporting
is unavailable, ask for a private contact channel without posting exploit
details. The latest tagged release
is the supported version for fixes.

The app defaults to loopback access. When serving other computers, use trusted
HTTPS and optional dashboard authentication. Miner firmware APIs can remain
unencrypted on a LAN. Database credentials and profile payloads are encrypted,
but the OS account that can read both the database and key can decrypt them.
Unix 0600 permissions do not translate to Windows ACLs; restrict access to
the Windows user account's configuration directory.

Release archives include SHA-256 checksums. They are not currently signed or
notarized. Checksums detect a mismatched download; they do not establish trust
independently of the GitHub release containing them.
