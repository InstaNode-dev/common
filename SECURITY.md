# Security Policy

## Reporting a vulnerability

If you believe you have found a security vulnerability in `common` or any
downstream InstaNode service that consumes it (api, worker, provisioner,
dashboard, MCP, SDK, CLI), please report it privately by emailing
**security@instanode.dev**. Do not open a public GitHub issue, do not file a
public PR with a fix, and do not disclose the issue on social media or in
chat channels until we have had a chance to investigate and ship a fix.

We will acknowledge receipt within **2 business days** and aim to provide an
initial assessment within **5 business days**. For critical vulnerabilities
(remote code execution, credential disclosure, tenant isolation breach, billing
bypass) we will ship a patch and coordinated disclosure within **30 days**.
For lower-severity issues we will work with you on a reasonable timeline.

## Scope

This repository contains shared Go packages — `crypto` (AES-256-GCM keyring,
JWT signing), `queueprovider` (NATS / RabbitMQ / Kafka credential issuance),
`storageprovider` (DO Spaces / R2 / S3 / MinIO credential issuance),
`readiness` (deep `/readyz` checks), `plans` (tier limits), `logctx`,
`buildinfo`, `resourcestatus`, and `resourcetype`. A bug in any of these
packages affects all three backend services, so we treat reports against this
repo with the same urgency as a report against api or provisioner.

In-scope issues include: cryptographic weaknesses, key-handling bugs,
tenant-isolation bypasses in the credential providers, secret leakage through
logs or readiness output, signature-verification flaws, and denial-of-service
vectors that can be triggered by a single tenant. Out of scope: bugs in
upstream dependencies (please report those upstream), self-inflicted issues
from running the code with development-mode secrets, and theoretical attacks
that require a pre-existing compromise of the host.

## Safe harbor

We will not pursue legal action against researchers who act in good faith,
who give us a reasonable opportunity to respond before disclosing publicly,
who do not access or exfiltrate data beyond what is necessary to demonstrate
the vulnerability, who do not perform attacks that degrade service for our
users (DoS, social engineering of staff, physical attacks), and who comply
with all applicable laws. We are happy to publicly credit researchers who
report responsibly.

## Contact

**security@instanode.dev**
