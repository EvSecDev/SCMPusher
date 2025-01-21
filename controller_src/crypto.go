// controller
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

func modifyVault(endpointName string) (err error) {
	// Ensure vault file exists, if not create it
	vaultFileMeta, err := os.Stat(config.VaultFilePath)
	if os.IsNotExist(err) {
		var vaultFile *os.File
		vaultFile, err = os.Create(config.VaultFilePath)
		if err != nil {
			return
		}
		vaultFileMeta, _ = vaultFile.Stat()
		vaultFile.Close()
	} else if err != nil {
		return
	}

	// Get unlock pass from user
	vaultPassword, err := promptUserForSecret("Enter password for vault: ")
	if err != nil {
		return
	}

	// Check if vault file already has data (size is larger than the header)
	vaultFileSize := vaultFileMeta.Size()
	if vaultFileSize > 28 {
		// Read in encrypted vault file
		var lockedVaultFile []byte
		lockedVaultFile, err = os.ReadFile(config.VaultFilePath)
		if err != nil {
			err = fmt.Errorf("failed to retrieve vault file: %v", err)
			return
		}

		// Decrypt Vault
		var unlockedVault string
		unlockedVault, err = decrypt(lockedVaultFile, vaultPassword)
		if err != nil {
			return
		}

		// Unmarshal vault JSON into global struct
		err = json.Unmarshal([]byte(unlockedVault), &config.Vault)
		if err != nil {
			return
		}
	}

	// Get password from user for host
	loginUserName := config.HostInfo[endpointName].EndpointUser
	hostPassword, err := promptUserForSecret("Enter '%s' password for host '%s' (leave empty to delete entry): ", loginUserName, endpointName)
	if err != nil {
		return
	}

	// Remove password if user supplied empty password
	if hostPassword == "" {
		// Just return if host is not in vault
		_, hostExistsinVault := config.Vault[endpointName]
		if !hostExistsinVault {
			return
		}

		// Confirm with user before deleting vault entry
		var userResponse string
		userResponse, err = promptUser("Please type 'y' to delete vault host '%s': ", endpointName)
		if err != nil {
			return
		}
		// Check if the user typed 'y' (always lower-case)
		if userResponse == "y" {
			// Remove vault entry for host
			delete(config.Vault, endpointName)
			return
		} else {
			fmt.Printf("Did not receive confirmation, exiting.\n")
			return
		}
	}

	// Ask again to confirm
	hostPasswordConfirm, err := promptUserForSecret("Enter '%s' password for host '%s' again: ", loginUserName, endpointName)
	if err != nil {
		return
	}

	// Error if entered passwords are not identical
	if hostPassword != hostPasswordConfirm {
		err = fmt.Errorf("passwords do not match")
		return
	}

	// Modify/Add host password
	var credential Credential
	credential.LoginUserPassword = hostPassword
	config.Vault[endpointName] = credential

	// Encrypt and write changes to vault file - return with or without error
	err = lockVault(vaultPassword)
	return
}

// Encrypts and writes current vault data back to vault file
func lockVault(vaultPassword string) (err error) {
	// Marshal vault into json
	unlockedVault, err := json.Marshal(config.Vault)
	if err != nil {
		return
	}

	// Encrypt Vault
	lockedVault, err := encrypt(unlockedVault, vaultPassword)
	if err != nil {
		return
	}

	// Write encrypted vault back to disk - return with or without error
	err = os.WriteFile(config.VaultFilePath, lockedVault, 0600)
	return
}

// Opens vault and retrieves password for remote host
func unlockVault(endpointName string) (hostPassword string, err error) {
	printMessage(VerbosityFullData, "      Host requires password, unlocking vault\n")

	// Open vault if not already open - should only happen once since vault is global
	if len(config.Vault) == 0 {
		printMessage(VerbosityFullData, "      Reading vault file\n")

		// Read in encrypted vault file
		var lockedVaultFile []byte
		lockedVaultFile, err = os.ReadFile(config.VaultFilePath)
		if err != nil {
			err = fmt.Errorf("failed to retrieve vault file: %v", err)
			return
		}

		// Get unlock pass from user
		var vaultPassword string
		vaultPassword, err = promptUserForSecret("Enter password for vault: ")
		if err != nil {
			return
		}

		printMessage(VerbosityFullData, "      Decrypting vault\n")

		// Decrypt Vault
		var unlockedVault string
		unlockedVault, err = decrypt(lockedVaultFile, vaultPassword)
		if err != nil {
			return
		}

		// Unmarshal vault JSON using global struct
		err = json.Unmarshal([]byte(unlockedVault), &config.Vault)
		if err != nil {
			return
		}
	}

	printMessage(VerbosityFullData, "      Retrieving password from vault\n")

	// Double check host is in vault
	_, hostHasVaultEntry := config.Vault[endpointName]
	if !hostHasVaultEntry {
		err = fmt.Errorf("host does not have an entry in the vault")
		return
	}

	// Retrieve password for this host
	hostPassword = config.Vault[endpointName].LoginUserPassword
	return
}

// Takes a string input, and returns a SHA256 hexadecimal hash string
func SHA256Sum(input string) (hash string) {
	// Convert input string to byte array
	inputBytes := []byte(input)

	// Create new hashing function
	hasher := sha256.New()

	// Write input bytes into hasher
	hasher.Write(inputBytes)

	// Retrieve the raw hash
	rawHash := hasher.Sum(nil)

	// Format raw hash into hex
	hash = hex.EncodeToString(rawHash)

	return
}

// Derive a secure key from a password string using argon2
func deriveKey(password string, salt []byte) (derivedKey []byte) {
	// Argon2 parameters
	const time = 1
	const memory = 64 * 1024
	const threads = 4
	const keyLength = 32

	// Derive the key from the password
	derivedKey = argon2.IDKey([]byte(password), salt, time, memory, threads, keyLength)
	return
}

// Encrypt a string using a password with chacha20poly1305 and return a byte array of cipher text with required salt and nonce
func encrypt(plainTextBytes []byte, decryptPassword string) (cipherTextSaltNonce []byte, err error) {
	printMessage(VerbosityDebug, "  Password to Encrypt: %s\n", decryptPassword)
	printMessage(VerbosityDebug, "  PlainText: %v\n", string(plainTextBytes))

	// Generate a salt
	salt := make([]byte, 16) // 16 bytes salt
	if _, err = io.ReadFull(rand.Reader, salt); err != nil {
		return
	}

	// Derive the encryption key using Argon2
	key := deriveKey(decryptPassword, salt)

	// Create a new ChaCha20-Poly1305 instance
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return
	}

	// Generate a nonce (12 bytes for ChaCha20-Poly1305)
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return
	}

	printMessage(VerbosityDebug, "    Salt: %v\n", salt)
	printMessage(VerbosityDebug, "    Nonce: %v\n", nonce)
	printMessage(VerbosityDebug, "    Key: %v\n", key)

	// Encrypt the plaintext
	ciphertext := aead.Seal(plainTextBytes[:0], nonce, plainTextBytes, nil)

	printMessage(VerbosityDebug, "    CipherText: %v\n", ciphertext)

	// The final ciphertext will include the salt and nonce for later decryption
	cipherTextSaltNonce = append(salt, append(nonce, ciphertext...)...)

	printMessage(VerbosityDebug, "    CipherText with Salt and Nonce: %v\n", cipherTextSaltNonce)

	// Encode byte array to base64
	encodedCipherText := base64.StdEncoding.EncodeToString(cipherTextSaltNonce)
	cipherTextSaltNonce = []byte(encodedCipherText)

	printMessage(VerbosityDebug, "    Encoded CipherText: %v\n", cipherTextSaltNonce)

	return
}

// Decrypt a byte array using a password with chacha20poly1305 and return a string of plain text
func decrypt(cipherTextSaltNonce []byte, encryptPassword string) (plainText string, err error) {
	printMessage(VerbosityDebug, "  Password to Decrypt: %s\n", encryptPassword)
	printMessage(VerbosityDebug, "  Encoded CipherText: %v\n", cipherTextSaltNonce)

	// Decode base64 to raw byte array
	cipherTextSaltNonce, err = base64.StdEncoding.DecodeString(string(cipherTextSaltNonce))
	if err != nil {
		err = fmt.Errorf("failed to decode cipher text from base64: %v", err)
		return
	}

	printMessage(VerbosityDebug, "    CipherText with Salt and Nonce: %x\n", cipherTextSaltNonce)

	// Extract the salt (16 bytes) and nonce (12 bytes) from the ciphertext
	salt := cipherTextSaltNonce[:16]
	nonce := cipherTextSaltNonce[16:28]
	cipherTextBytes := cipherTextSaltNonce[28:]

	// Derive the decryption key using Argon2
	key := deriveKey(encryptPassword, salt)

	printMessage(VerbosityDebug, "    CipherText: %v\n", cipherTextBytes)
	printMessage(VerbosityDebug, "    Key: %v\n", key)
	printMessage(VerbosityDebug, "    Nonce: %v\n", nonce)
	printMessage(VerbosityDebug, "    Salt: %v\n", salt)

	// Create a new ChaCha20-Poly1305 instance
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return
	}

	// Decrypt the ciphertext
	plainTextBytes, err := aead.Open(nil, nonce, cipherTextBytes, nil)
	if err != nil {
		return
	}

	plainText = string(plainTextBytes)
	printMessage(VerbosityDebug, "    PlainText: %s\n", plainText)
	return
}