# Gateway 0.3.1 Mac reviewer submission

The reviewer uses a local copy of the private handoff packet and the
purpose-enrolled Ed25519 private key on their own computer. The command checks
every packet file against the fixed manifest, recomputes the 895-byte subject,
checks the purpose-specific trust file against authenticated CLI `main`, and
requires the reviewer's GitHub login to be exactly `fredoliveira` or
`goncalossilva`. It verifies the SSH signature locally before posting it to
an open issue in the private `sovereign46/models-cli` repository, then reads
the persisted comment back and checks its author and exact body.

```sh
python3 scripts/gateway_direct_mac_submit.py \
  --packet /absolute/path/to/20261105-ef9-v3-reviewer-handoff \
  --key /absolute/path/to/enrolled-ed25519-private-key \
  --issue https://github.com/sovereign46/models-cli/issues/ISSUE_NUMBER
```

The issue body must include the exact subject SHA-256
`e913b6ca19523abb0a6326143ba55b48d00a7320491f7af735ce1820f452eb7c`,
packet manifest SHA-256
`e3fe07a3beaf165890ba7042e6709f6269d0da797589d63acb8ba00cebefb64c`,
and canonical trust file SHA-256. The latter can be set only after Fred and
Gonçalo explicitly accept distinct keys for the
`s46-gateway-direct-mac-approval-v1` purpose. The current trust template is
empty, so the command fails closed until enrollment. The original packet
manifest and approval subject remain unchanged.

The private issue comment is a transport receipt, not independent release
authority. The coordinator must authenticate both comment authors, extract
their exact SSH signatures, and rerun the Gateway approval verifier against
the exact subject and explicitly enrolled trust before publication.
