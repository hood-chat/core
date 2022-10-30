package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	ds "github.com/ipfs/go-datastore"
	dsync "github.com/ipfs/go-datastore/sync"
	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	rhost "github.com/libp2p/go-libp2p/p2p/host/routed"

	options "github.com/ipfs/interface-go-ipfs-core/options"
	config "github.com/ipfs/kubo/config"
	serialize "github.com/ipfs/kubo/config/serialize"
	"github.com/ipfs/kubo/core/bootstrap"
)

const (
	algorithmDefault    = options.Ed25519Key
	algorithmOptionName = "algorithm"
	bitsOptionName      = "bits"
	emptyRepoOptionName = "empty-repo"
	profileOptionName   = "profile"
)

func Create(ctx context.Context, configRoot string) (*rhost.RoutedHost, error) {
	// Now, normally you do not just want a simple host, you want
	// that is fully configured to best support your p2p application.
	// Let's create a second host setting some more options.
	// Set your own keypair
	con, err := connmgr.NewConnManager(10, 100)
	if err != nil {
		panic(err)
	}

	if !configIsInitialized(configRoot) {
		var conf *config.Config

		if conf == nil {
			identity, err := config.CreateIdentity(os.Stdout, []options.KeyGenerateOption{
				options.Key.Type(algorithmDefault),
			})
			if err != nil {
				panic(err)
			}
			conf, err = config.InitWithIdentity(identity)
			if err != nil {
				panic(err)
			}
		}
		err = doInit(os.Stdout, configRoot, conf)
		if err != nil {
			panic(err)
		}
	}

	cfg, err := openConfig(configRoot)
	if err != nil {
		panic(err)
	}
	sk, err := cfg.Identity.DecodePrivateKey("passphrase todo!")
	if err != nil {
		panic(err)
	}

	opt := []libp2p.Option{
		libp2p.DefaultTransports,
		libp2p.DefaultSecurity,
		// Use the keypair we generated
		libp2p.Identity(sk),
		// Multiple listen addresses
		libp2p.DefaultListenAddrs,
		// Let's prevent our peer from having too many
		// connections by attaching a connection manager.
		libp2p.ConnectionManager(con),
		// libp2p.DefaultMuxers,
		// Let this host use relays and advertise itself on relays if
		// it finds it is behind NAT. Use libp2p.Relay(options...) to
		// enable active relays and more.
		// libp2p.EnableAutoRelay(),
		libp2p.EnableAutoRelay(),
		// If you want to help other peers to figure out if they are behind
		// NATs, you can launch the server-side of AutoNAT too (AutoRelay
		// already runs the client)
		//
		// This service is highly rate-limited and should not cause any
		// performance issues.
		libp2p.EnableNATService(),
		libp2p.EnableHolePunching(),
	}

	basicHost, err := libp2p.New(opt...)
	if err != nil {
		return nil, err
	}

	// Construct a datastore (needed by the DHT). This is just a simple, in-memory thread-safe datastore.
	dstore := dsync.MutexWrap(ds.NewMapDatastore())

	// Make the DHT
	kDht := dht.NewDHT(ctx, basicHost, dstore)
	cfg.Bootstrap = append(cfg.Bootstrap,
		"/ip4/34.224.40.105/udp/4001/quic/p2p/12D3KooWEftKAarKSc1bhQfgn5aoW5UnaSqCr9UMhRoqhsBA6MmX",
		"/ip4/54.235.11.104/udp/4001/quic/p2p/12D3KooWEHmZunko2dupAR9J3Ydo3yN8aW7oZWkAxv5zsNL7UPRH",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
		"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
	)

	bootstrapPeers, _ := cfg.BootstrapPeers()
	btconf := bootstrap.BootstrapConfigWithPeers(bootstrapPeers)
	btconf.MinPeerThreshold = 2

	// connect to the chosen ipfs nodes
	_, err = bootstrap.Bootstrap(peer.ID(cfg.Identity.PeerID), basicHost, kDht, btconf)
	if err != nil {
		log.Error("bootstrap failed. ", err)
		return nil, err
	}
	// Make the routed host
	routedHost := rhost.Wrap(basicHost, kDht)

	log.Infof("Fula Bootsraped and ready with ID:", routedHost.ID())
	return routedHost, nil
}

func doInit(out io.Writer, repoRoot string, conf *config.Config) error {
	if _, err := fmt.Fprintf(out, "initializing IPFS node at %s\n", repoRoot); err != nil {
		return err
	}

	if err := checkWritable(repoRoot); err != nil {
		return err
	}

	if err := initConfig(repoRoot, conf); err != nil {
		return err
	}

	return nil
}

func initConfig(path string, conf *config.Config) error {
	if configIsInitialized(path) {
		return nil
	}
	configFilename, err := config.Filename(path, "")
	if err != nil {
		return err
	}
	// initialization is the one time when it's okay to write to the config
	// without reading the config from disk and merging any user-provided keys
	// that may exist.
	if err := serialize.WriteConfigFile(configFilename, conf); err != nil {
		return err
	}

	return nil
}

func checkWritable(dir string) error {
	_, err := os.Stat(dir)
	if err == nil {
		// dir exists, make sure we can write to it
		testfile := filepath.Join(dir, "test")
		fi, err := os.Create(testfile)
		if err != nil {
			if os.IsPermission(err) {
				return fmt.Errorf("%s is not writeable by the current user", dir)
			}
			return fmt.Errorf("unexpected error while checking writeablility of repo root: %s", err)
		}
		fi.Close()
		return os.Remove(testfile)
	}

	if os.IsNotExist(err) {
		// dir doesn't exist, check that we can create it
		return os.Mkdir(dir, 0775)
	}

	if os.IsPermission(err) {
		return fmt.Errorf("cannot write to %s, incorrect permissions", err)
	}

	return err
}

func configIsInitialized(path string) bool {
	configFilename, err := config.Filename(path, "")
	if err != nil {
		return false
	}
	if fileExists(configFilename) {
		return true
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func openConfig(path string) (*config.Config, error) {
	configFilename, err := config.Filename(path, "")
	if err != nil {
		return nil, err
	}
	conf, err := serialize.Load(configFilename)
	if err != nil {
		return nil, err
	}

	return conf, err
}