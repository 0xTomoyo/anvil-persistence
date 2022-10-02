# anvil-persistence

Create persistent Anvil instances.

The `anvil-persistence` command spins up a persistent Anvil instance. Any updates to the Anvil state are immediately saved to disk (by default to the `anvil_state.txt` file). When rerunning the command (with the same file), the state is automatically loaded into the Anvil instance.

## Install

Requirements: [Go](https://go.dev/doc/install) and [Foundry](https://github.com/foundry-rs/foundry#installation).

```
go install
```

## Usage

```
anvil-persistence
```

```
anvil-persistence -command=/root/.foundry/bin/anvil -file=anvil_state.txt -host=127.0.0.1
```