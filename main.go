package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// The command to start Anvil
const anvilCommandDefault string = "anvil"

// File containing the Anvil state
const anvilStateDefault string = "anvil_state.txt"

// The Anvil ipc path
const ipcPath string = "/tmp/anvil.ipc"

// The message at which Anvil starts up
const startupMessage string = "Listening on"

// A snapshot of the Anvil state
type AnvilSnapshot struct {
	BlockNumber uint64
	State       string
}

func main() {
	// Setup context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Parse the flags
	var (
		anvilCommand string
		anvilState   string
		host         string
	)

	flag.StringVar(&anvilCommand, "command", anvilCommandDefault, "The command to start Anvil")
	flag.StringVar(&anvilState, "file", anvilStateDefault, "File containing the Anvil state")
	flag.StringVar(&host, "host", "127.0.0.1", "Host IP address")

	flag.Parse()

	// Anvil executable
	anvil := exec.Command(anvilCommand, "--host", host, "--ipc")

	// Output pipe of the Anvil process
	stdout, err := anvil.StdoutPipe()
	if err != nil {
		panic(err)
	}

	// Start the Anvil process
	if err := anvil.Start(); err != nil {
		panic(err)
	}

	// Notifies that the anvil process has started
	start := make(chan struct{})

	// Print the output of the Anvil process
	go func() {
		var started bool
		scanner := bufio.NewScanner(stdout)

		for scanner.Scan() {
			m := scanner.Text()
			fmt.Println(m)

			// Notify the start channel that Anvil has started
			if strings.HasPrefix(m, startupMessage) && !started {
				close(start)
				started = true
			}
		}
	}()

	// Wait for the Anvil process to exit
	go func() {
		if err := anvil.Wait(); err != nil {
			panic(err)
		}
	}()

	// Kill the Anvil process on exit
	defer func() {
		if err := anvil.Process.Kill(); err != nil {
			panic(err)
		}
	}()

	// Wait for the Anvil process to start
	select {
	case <-start:
		fmt.Println("Started the Anvil process")
	case <-ctx.Done():
		return
	}

	// Connect to the Anvil ipc with a timeout
	dCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c, err := rpc.DialContext(dCtx, ipcPath)
	if err != nil {
		panic(err)
	}
	client := ethclient.NewClient(c)
	defer client.Close()

	fmt.Println("Connected to the Anvil process")

	// Channels for communicating with the snapshot capture routine
	snapshotCh := make(chan uint64, 1)
	savedSnapshotCh := make(chan struct{})

	// Start the snapshot capture goroutine to capture and save snapshots to disk
	go func() {
		for {
			blockNumber := <-snapshotCh

			// Dump the Anvil state
			var result string
			err = c.Call(&result, "anvil_dumpState")
			if err != nil {
				panic(err)
			}

			// Encode the Anvil state
			var encodeBuffer bytes.Buffer
			if err := gob.NewEncoder(&encodeBuffer).Encode(&AnvilSnapshot{
				BlockNumber: blockNumber,
				State:       result,
			}); err != nil {
				panic(err)
			}

			// Write the Anvil state
			if err := os.WriteFile(anvilState, encodeBuffer.Bytes(), 0644); err != nil {
				panic(err)
			}

			fmt.Printf("Captured snapshot at block %d\n", blockNumber)

			// Notify that we have captured a snapshot
			savedSnapshotCh <- struct{}{}
		}
	}()

	// Load the saved Anvil state
	data, err := os.ReadFile(anvilState)

	if len(data) == 0 || err != nil {
		fmt.Println("No Anvil state found")

		// Capture a snapshot on startup if we don't have any saved state
		blockNumber, err := client.BlockNumber(ctx)
		if err != nil {
			panic(err)
		}
		snapshotCh <- blockNumber
		<-savedSnapshotCh
	} else {
		// Decode the saved state
		snapshot := &AnvilSnapshot{}
		decodeBuffer := bytes.NewBuffer(data)
		if err := gob.NewDecoder(decodeBuffer).Decode(snapshot); err != nil {
			panic(err)
		}

		// Mine blocks to ensure that our block number matches the state
		if snapshot.BlockNumber != 0 {
			err = c.Call(nil, "anvil_mine", snapshot.BlockNumber, 0)
			if err != nil {
				panic(err)
			}
		}

		// Load the Anvil state
		var result bool
		err = c.Call(&result, "anvil_loadState", string(snapshot.State))
		if err != nil {
			panic(err)
		}

		if result {
			fmt.Printf("Loaded the Anvil state at block number %d\n", snapshot.BlockNumber)
		} else {
			panic(errors.New("failed to load the Anvil state"))
		}
	}

	// Subscribe to new blocks
	newHeadCh := make(chan *types.Header)
	subscription, err := client.SubscribeNewHead(ctx, newHeadCh)
	if err != nil {
		panic(err)
	}
	defer subscription.Unsubscribe()

	fmt.Println("Subscribed to new blocks")

	var (
		snapshot        bool
		pendingSnapshot uint64
	)

	// Routine for taking snapshots as new blocks arrive
	for {
		select {
		case header := <-newHeadCh:
			if snapshot {
				// If we are currently taking a snapshot, take a new one once it current one is done
				pendingSnapshot = header.Number.Uint64()
			} else {
				// Take a new snapshot
				snapshotCh <- header.Number.Uint64()
				snapshot = true
				pendingSnapshot = 0
			}
		case <-savedSnapshotCh:
			if pendingSnapshot == 0 {
				// The last snapshot has completed and there are no pending snapshots to take
				snapshot = false
			} else {
				// If we received new blocks after starting the last snapshot, take a new snapshot
				snapshotCh <- pendingSnapshot
				pendingSnapshot = 0
			}
		case err := <-subscription.Err():
			fmt.Printf("Subscription err: %v\n", err)
		case <-ctx.Done():
			// If we are currently taking a snapshot, drain the snapshot saved channel
			if snapshot {
				<-savedSnapshotCh
			}

			// Get the latest block number
			cCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			blockNumber, err := client.BlockNumber(cCtx)
			if err != nil {
				panic(err)
			}

			// Take a new snapshot
			snapshotCh <- blockNumber
			<-savedSnapshotCh

			return
		}
	}
}
