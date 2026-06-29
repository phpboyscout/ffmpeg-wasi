---
title: Release signing
description: How ffmpeg-wasi releases are signed — an AWS KMS key only the tag pipeline can wield, a detached signature over checksums.txt, what it defends, and where the public key lives.
date: 2026-06-29
tags: [explanation, signing, security, releases]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Release signing

Every ffmpeg-wasi release is **signed**, so a consumer can be sure the module they load is the
one this project published — not a substitute. The mechanism is deliberately small and
auditable.

## What is signed

The release publishes `checksums.txt` — the SHA-256 of every asset, **including
`provenance.json`** — and a detached signature over it, `checksums.txt.sig`. Because the
signature covers `checksums.txt`, and `checksums.txt` covers everything else, **one signature
certifies the whole release**: verify the signature, then check any asset against its line in
`checksums.txt`.

`checksums.txt.sig` is a small JSON envelope:

```json
{ "key_id": "1698ceea…", "algorithm": "RSASSA_PSS_SHA_256", "signature": "<base64>" }
```

The `signature` is a raw RSASSA-PSS (SHA-256) signature; `key_id` names which key signed.

## How it is signed — only the tag pipeline can

The signing key is an **asymmetric AWS KMS key** (RSA-4096, `SIGN_VERIFY`). Its **private half
never leaves KMS** — there is no key file, and no human ever holds it. Signing happens only in
the tag-gated `sign` CI job, which assumes an IAM role via **GitLab OIDC**; that role's trust
policy is pinned to *this project's* release tags
(`project_path:phpboyscout/ffmpeg-wasi:ref_type:tag:ref:n*`). So:

- a leaked credential cannot sign — there is no static credential;
- the infra apply pipeline cannot sign — it can manage the key resource but has no `kms:Sign`;
- branch, MR, and non-`n*`-tag pipelines cannot sign — they fail the OIDC subject filter.

The key is provisioned in [phpboyscout/infra](https://gitlab.com/phpboyscout/infra)
(`src/main.signing-kms.tf`) and is **dedicated to ffmpeg-wasi** — not shared with any other
project, so no other project's pipeline can ever produce an ffmpeg-wasi signature.

## The public key

The verifying key's **key-id** — the hex SHA-256 of its SubjectPublicKeyInfo DER, which is what
`checksums.txt.sig` names — is:

```
1698ceea3728c7e5cc89288675e643c1e9b6110ae88575aeaa15148eb9630a76
```

The primary consumer, [afmpeg](https://gitlab.com/phpboyscout/afmpeg), **embeds and pins this
key**, so its `WithModuleRelease` verifies releases automatically — the trust root ships *inside*
the verifying binary, which is stronger than any key fetched at runtime.

Deliberately, the public key is **not** published in this repository: a key you fetch from the
same platform that hosts the releases is not an independent anchor — a compromise of that
platform would control both. Its authoritative public location for third-party verification is a
**Web Key Directory on `phpboyscout.uk`** — a control plane independent of GitLab — which is the
subject of afmpeg spec
[0011](https://gitlab.com/phpboyscout/afmpeg/-/blob/main/docs/development/specs/0011-wkd-attestation.md)
(a committed fast-follow). A by-hand verification guide ships with it.

## What it defends — and what it does not

The signature defends against a swapped or tampered artifact: leaked credentials, a compromised
apply runner, and non-release pipelines all **cannot** produce a valid signature.

It does **not**, on its own, defend against a **compromised GitLab account that can push a
tag** — that triggers the legitimate release pipeline, which would sign a malicious build with
the real key. Closing that "poisoned well" needs a second, independent attestation rooted in a
control plane GitLab cannot touch (the `phpboyscout.uk` domain); that is a committed fast-follow,
tracked as afmpeg spec
[0011](https://gitlab.com/phpboyscout/afmpeg/-/blob/main/docs/development/specs/0011-wkd-attestation.md).
Stating the gap plainly is part of the posture.

## Rotation

The key alias is versioned (`…-v1`). Rotation mints a new key, publishes the new public key via
the WKD location above, and adds it to afmpeg's pinned set alongside the old one for an overlap
window before the old key is retired — so there is no flag-day, and a compromised key can be
dropped promptly.

## The tooling is MIT

`build/sign-release.sh` only *orchestrates* — it shells `aws kms sign`. Like the rest of `build/`
it is MIT, and it links nothing: the signature is over text, the key is in KMS.
