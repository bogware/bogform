# Working with Bogform in this repository

This repository manages its infrastructure with [Bogform](https://github.com/bogware/bogform), an
open-source fork of OpenTofu. This file tells people and coding agents how the setup works and what
the rules are. Read it before changing anything in the infrastructure directory or its pipeline.

The canonical copy is `templates/BOGFORM.md` in bogware/bogform. When you adopt Bogform in another
repository, copy that file, then fill in **This repository** at the bottom.

## How it works

- **Bogform is a drop-in for Terraform and OpenTofu.** The CLI is `bogform`. It reads `.tf` and
  `.tofu` files (and `.bog`, which takes precedence), the same providers from
  registry.opentofu.org, and `TF_*` environment variables (`BOG_*` takes precedence). Terraform
  documentation for the language and providers applies.
- **State is in this repository, encrypted.** The `git` backend keeps state on the
  `bogform-state` branch and takes locks by pushing `bogform-locks/*` branches. The `encryption`
  block encrypts state and plans with a passphrase, and `enforced = true` refuses to write anything
  unencrypted. Without the passphrase, the state can't be read. Every version of the state stays
  on the branch, so a bad change to the state can be undone.
- **The passphrase finds itself.** The `passphrase` key provider reads it from an environment
  variable in the pipeline, set from a secret, and from the OS keychain or the cloud (SSM or Key
  Vault) on a workstation. Older setups pass it in as `var.state_passphrase` instead.
- **Changes go through pull requests.** A pull request that touches the infrastructure gets a plan,
  posted as a comment, using a read-only cloud role. Changes are applied by the pipeline, using a
  role that can only change this project's resources, either when the pull request merges or when
  someone runs the workflow on the default branch. **This repository** says which.
- **No stored cloud keys.** The pipeline signs in to AWS with GitHub's OIDC tokens. The plan and
  apply roles, and what each may do, are defined in bogware/aws-shared, not here.

## Rules

1. **Don't apply from a workstation** unless a person explicitly asks you to. Merge, and let the
   pipeline apply. Local `plan` is fine; use `-lock=false` so it doesn't take the state lock.
2. **Never print, log or commit secrets**, including the state passphrase. Read it into an
   environment variable, and don't echo it.
3. **Keep personal information out of the repository**, including email addresses. Pass values
   like that in as `sensitive = true` variables, set from pipeline secrets. Remember that plan
   output is posted to pull requests: a value that's not sensitive, or that's used as an import
   ID, appears there.
4. **Don't touch the `bogform-state` or `bogform-locks/*` branches** by hand. If a lock is stuck
   because a run was cancelled, check that nothing is running, then ask a person before running
   `bogform force-unlock`. To undo a bad change to the state, ask a person, then use
   `bogform state history` and `bogform state restore`.
5. **Review the plan before merging.** Look for anything that's replaced or destroyed. A change to
   a resource's name or key often means a replacement; use a `moved` block to avoid it.
6. **If the pipeline gets `AccessDenied`**, the plan or apply role is missing a permission. Change
   the role in bogware/aws-shared, in its own pull request. Don't work around it here.
7. **Commit `.terraform.lock.hcl`.** Don't commit `.terraform/`, `.bogform-ci/`, plan files or
   build output.

## Making a change

```sh
cd <infrastructure directory>
# Set any variables that come from secrets (see "This repository"). The passphrase key provider
# reads the state passphrase itself, from your keychain or the cloud.

bogform init
bogform fmt
bogform validate
bogform plan -lock=false
```

Then open a pull request, read the plan comment, and merge.

The first time on a workstation, if the passphrase key provider lists a `keychain` entry, store the
passphrase there once. For example, from its backup in SSM:

```sh
aws ssm get-parameter --name <passphrase parameter> --with-decryption \
  --query Parameter.Value --output text | bogform state passphrase set <keychain entry>
```

If it reads `aws_ssm` instead, your AWS credentials are enough. Older setups that use
`var.state_passphrase` need `TF_VAR_state_passphrase` set, read from the backup without echoing it.

### Rotating the passphrase

Add the new passphrase under new names, with the old method as a `fallback`, then run
`bogform state rekey` and remove the fallback. Earlier versions on the state branch stay encrypted
with the old passphrase, so if it leaked, treat them as leaked too. Ask a person first: this writes
the state.

### Adopting existing resources

To bring resources that already exist under management, write `import` blocks (`to` and `id`)
next to the configuration. The plan shows them as "will be imported". After they've been applied,
the blocks do nothing, so remove them in a follow-up change. An import ID must not be sensitive,
and it's shown in the plan, so import anything personal from a workstation instead.

### Upgrading Bogform or providers

Bump the version in the workflow's `bogware/bogform` step (both the action's tag and its `version`
input), and provider versions in `required_providers`, then run `bogform init -upgrade` and commit
the lock file. Dependency updates are always manual: don't set up scheduled update bots.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `couldn't find the state passphrase` | None of the passphrase key provider's sources has it. The error lists each one. Store it with `bogform state passphrase set`, or check your AWS credentials. |
| `Unable to compute static value ... state_passphrase` | An older setup, and the passphrase isn't set. Set `TF_VAR_state_passphrase`. |
| The state is wrong after a change, such as a bad `state rm` | `bogform state history` lists earlier versions, and `bogform state restore <version>` brings one back. Ask a person first. |
| State can't be decrypted | The wrong passphrase. Check the secret against the backup. |
| `Error acquiring the state lock` | A run is in progress, or one was cancelled and left its lock. See rule 4. |
| A Lambda function changes on every plan | Its package isn't reproducible. Set `output_file_mode = "0644"` on the `archive_file`. |
| A value looks missing in the plan role's view | The read-only role can't read data (for example, it can't decrypt Lambda environments). Check the apply, not the plan. |

## This repository

<!-- Fill this in when you copy the file. -->

| | |
|---|---|
| Infrastructure directory | |
| Pipeline | |
| Applies | |
| Plan role | |
| Apply role | |
| Secrets | |
| Passphrase backup | |
| Keychain entry | |
| Defined in bogware/aws-shared | |

Anything else specific to this repository:

-
