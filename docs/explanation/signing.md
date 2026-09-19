---
title: Release signing
description: "How ffmpeg-wasi releases are signed: an AWS KMS key only the tag pipeline can wield, a detached signature over checksums.txt, what it defends, and where the public key lives."
date: 2026-06-29
tags: [explanation, signing, security, releases]
authors: [Matt Cockayne <matt@phpboyscout.uk>]
---

# Release signing

Every ffmpeg-wasi release is **signed**, so a consumer can be sure the module they load is the
one this project published, not a substitute. The mechanism is deliberately small and
auditable.

## What is signed

The release publishes `checksums.txt` (the SHA-256 of every asset (the `.wasm` modules and the
native drivers alike, **including `provenance.json`**) and a detached signature over it,
`checksums.txt.sig`. Because the
signature covers `checksums.txt`, and `checksums.txt` covers everything else, **one signature
certifies the whole release**: verify the signature, then check any asset against its line in
`checksums.txt`.

`checksums.txt.sig` is an **ASCII-armored OpenPGP detached signature**, the same format the
rest of the org uses (go-tool-base's `gtb update`), so any OpenPGP tool (`gpg --verify`, or
[afmpeg](https://gitlab.com/phpboyscout/afmpeg) via the `gitlab.com/phpboyscout/signing` module)
can verify it. The signature self-identifies the signing key by its fingerprint.

## How it is signed, and why only the tag pipeline can

The signing key is an **asymmetric AWS KMS key** (RSA-4096, `SIGN_VERIFY`). Its **private half
never leaves KMS**. There is no key file, and no human ever holds it. The tag-gated `sign` CI
job assumes an IAM role via **GitLab OIDC**, then runs **`sigillum`** (the org signing CLI):
`sigillum keys mint` derives the OpenPGP public key from the KMS key, and `sigillum sign` produces the detached
signature; KMS performs every private-key operation. The role's trust policy is pinned to *this
project's* release tags (`project_path:phpboyscout/ffmpeg-wasi:ref_type:tag:ref:n*`). So:

- a leaked credential cannot sign, because there is no static credential;
- the infra apply pipeline cannot sign; it can manage the key resource but has no `kms:Sign`;
- branch, MR, and non-`n*`-tag pipelines cannot sign, because they fail the OIDC subject filter.

The key is provisioned in [phpboyscout/infra](https://gitlab.com/phpboyscout/infra)
(`src/main.signing-kms.tf`) and is **dedicated to ffmpeg-wasi**, not shared with any other
project, so no other project's pipeline can ever produce an ffmpeg-wasi signature.

## The keys

Two OpenPGP keys back the chain (the go-tool-base model):

- the **signing key** (`ffmpeg-wasi-release-v2@phpboyscout.uk`), minted from the KMS key
  `alias/ffmpeg-wasi-release-signing-v2`. OpenPGP fingerprint
  `4C96ECB35C7446619FF78EB1ED1344E576B7BBBF`. Signs every release since 2026-07-24. Its
  predecessor (`ffmpeg-wasi-release@phpboyscout.uk`, `710881C1DDAEABD138E53004A2166E59EB6060E1`)
  signed every release before n8.1.2-11 alone and shared the signature with v2 from n8.1.2-11
  until September 2026; afmpeg still trusts it so those releases verify, see
  [Rotation](#rotation-and-why-a-release-may-carry-two-signatures).
- the **shared org rotation-authority key** (`release@phpboyscout.uk`,
  `2B26658409047ED08B56CEBDCF5B8DBB5D9F19C2`), an offline break-glass key that certifies the
  signing key and authorises rotation. One per org, never used in normal operation, and never a
  release signer.

The primary consumer, [afmpeg](https://gitlab.com/phpboyscout/afmpeg), **embeds and pins the
signing key** (and cross-checks it against WKD), so its `WithModuleRelease` verifies releases
automatically: the trust root ships *inside* the verifying binary, which is stronger than any key
fetched at runtime. The rotation-authority key stays offline org infrastructure: it backs key
rotation, not afmpeg's runtime trust set (afmpeg pins the signing key directly).

Deliberately, the public key is **not** published in this repository: a key you fetch from the
same platform that hosts the releases is not an independent anchor, since a compromise of that
platform would control both. Its authoritative public location for third-party verification is a
**Web Key Directory on `phpboyscout.uk`** (a control plane independent of GitLab), which is the
subject of afmpeg spec
[0011](https://gitlab.com/phpboyscout/afmpeg/-/blob/main/docs/development/specs/0011-wkd-attestation.md)
(a committed fast-follow). A by-hand verification guide ships with it.

## What it defends, and what it does not

The signature defends against a swapped or tampered artifact: leaked credentials, a compromised
apply runner, and non-release pipelines all **cannot** produce a valid signature.

It does **not** defend against a **compromised GitLab account that can push a tag**. That
triggers the legitimate release pipeline, which would sign a malicious build with the real key.
**No signing scheme closes that "poisoned well"**; it is the domain of GitLab account hardening
(protected tags, 2FA, required approvals) and reproducible builds, out of scope here. What the
**WKD second anchor** (afmpeg spec
[0011](https://gitlab.com/phpboyscout/afmpeg/-/blob/main/docs/development/specs/0011-wkd-attestation.md))
*does* add is **key/registry-substitution defense**: afmpeg cross-checks its embedded key against
a copy served from the `phpboyscout.uk` domain (a control plane independent of GitLab), so an
attacker would have to compromise *both*. Stating the limit plainly is part of the posture.

## Rotation, and why a release may carry two signatures

The key alias is versioned (`alias/ffmpeg-wasi-release-signing-v2` today). Rotation mints a new
key, publishes its public half under a **new** WKD identity, and adds it to afmpeg's pinned set
alongside the old one; releases are signed with both for an overlap window, then the old key stops
signing. There is no flag-day, and a compromised key can be dropped promptly.

The first rotation ran from 2026-07-24 to September 2026, when the AWS account holding the v1 key
was closed. Releases cut inside that window carry two detached signatures in the *same* armored
`checksums.txt.sig`, because `gtb sign --append` merges the second one:

- **It is still one file over the same `checksums.txt`.**
- **Verifying with either key succeeds.** An OpenPGP verifier skips signature packets from an issuer
  it does not know, so an afmpeg build that pins only v1 verifies those releases exactly as before,
  and one that pins both verifies with whichever it holds.
- **Neither key is preferred.** They certify identical bytes.

Releases since the window carry the v2 signature alone. The v1 **public** key stays in afmpeg's
trust set for as long as the releases it signed should verify, and the WKD entry for
`ffmpeg-wasi-release@phpboyscout.uk` is never edited: an afmpeg build cross-checks its embedded
set against the WKD set for its compiled-in identity and requires them to agree exactly, so a new
trust set always gets a new identity.

If you verify by hand a release from the window, expect `gpg --verify` to report an unknown-key
signature alongside the one it can check. That is the overlap, not a tampered manifest.

## The tooling is MIT

`build/sign-release.sh` only *orchestrates*: it shells `gtb keys mint` / `gtb sign`. Like the
rest of `build/` it is MIT, and it links nothing: the signature is over text, the key is in KMS.
