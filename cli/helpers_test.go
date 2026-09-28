package cli_test

import stded "crypto/ed25519"

func genKey() (stded.PublicKey, stded.PrivateKey, error) { return stded.GenerateKey(nil) }
