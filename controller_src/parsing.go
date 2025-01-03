// controller
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// Retrieves file names and associated host names for given commit
// Returns the changed files (file paths) between commit and previous commit
// Marks files with create/delete action for deployment and also handles marking symbolic links
func getCommitFiles(commit *object.Commit, DeployerEndpoints map[string]DeployerEndpoints, fileOverride string) (commitFiles map[string]string, commitHosts map[string]struct{}, err error) {
	// Show progress to user
	printMessage(VerbosityStandard, "Retrieving files from commit... \n")

	// Get the parent commit
	parentCommit, err := commit.Parents().Next()
	if err != nil {
		err = fmt.Errorf("failed retrieving parent commit: %v", err)
		return
	}

	// Get the diff between the commits
	patch, err := parentCommit.Patch(commit)
	if err != nil {
		err = fmt.Errorf("failed retrieving difference between commits: %v", err)
		return
	}

	// If file override array ispresent, split into fields
	var fileOverrides []string
	if len(fileOverride) > 0 {
		fileOverrides = strings.Split(fileOverride, ",")
	}

	printMessage(VerbosityProgress, "Parsing commit files\n")

	// Initialize maps
	commitFiles = make(map[string]string)
	commitHosts = make(map[string]struct{})

	// Determine what to do with each file in the commit
	for _, file := range patch.FilePatches() {
		// Get the old file and new file info
		from, to := file.Files()

		// Declare vars (to use named return err)
		var fromPath, toPath, commitFileToType string
		var SkipFromFile, SkipToFile bool

		// Validate the from File object
		fromPath, _, SkipFromFile, err = validateCommittedFiles(commitHosts, DeployerEndpoints, from)
		if err != nil {
			return
		}

		// Validate the to File object
		toPath, commitFileToType, SkipToFile, err = validateCommittedFiles(commitHosts, DeployerEndpoints, to)
		if err != nil {
			return
		}

		// Skip if either from or to file is not valid
		if SkipFromFile || SkipToFile {
			continue
		}

		// Skip file if not user requested file (if requested)
		if len(fileOverrides) > 0 {
			var fileNotRequested bool
			for _, overrideFile := range fileOverrides {
				if fromPath == overrideFile || toPath == overrideFile {
					continue
				}
				fileNotRequested = true
			}
			if fileNotRequested {
				continue
			}
		}

		// Add file to map depending on how it changed in this commit
		if from == nil {
			// Newly created files
			//   like `touch etc/file.txt`
			commitFiles[toPath] = "create"
		} else if to == nil {
			// Deleted Files
			//   like `rm etc/file.txt`
			commitFiles[fromPath] = "delete"
		} else if fromPath != toPath {
			// Copied or renamed files
			//   like `cp etc/file.txt etc/file2.txt` or `mv etc/file.txt etc/file2.txt`
			_, err := os.Stat(fromPath)
			if os.IsNotExist(err) && toPath == "" {
				// Mark for deletion if no longer present in repo
				commitFiles[fromPath] = "delete"
			}
			commitFiles[toPath] = "create"
		} else if fromPath == toPath {
			// Editted in place
			//   like `nano etc/file.txt`
			commitFiles[toPath] = "create"
		} else {
			// Anything else - no idea why this would happen
			commitFiles[fromPath] = "unsupported"
		}

		// Check for new symbolic links and add target file in actions for creation on remote hosts
		if commitFileToType == "symlink" && commitFiles[toPath] == "create" {
			// Get the target path of the sym link target and ensure it is valid
			var targetPath string
			targetPath, err = ResolveLinkToTarget(toPath)
			if err != nil {
				err = fmt.Errorf("failed resolving symbolic link")
				return
			}

			// Add new action to this file that includes the expected target path for the link
			commitFiles[toPath] = "symlinkcreate to target " + targetPath
		}
	}

	return
}

// Retrieves all files for current commit (regardless if changed)
// This is used to also get all files in commit for deployment of unchanged files when requested
func getRepoFiles(tree *object.Tree, deployerEndpoints map[string]DeployerEndpoints, fileOverride string) (commitFiles map[string]string, commitHosts map[string]struct{}, err error) {
	// Initialize maps
	commitFiles = make(map[string]string)
	commitHosts = make(map[string]struct{})

	// Get list of all files in repo tree
	allFiles := tree.Files()

	printMessage(VerbosityProgress, "Retrieving all files in repository\n")

	// Use all repository files to create map and array of files/hosts
	for {
		// Go to next file in list
		var repoFile *object.File
		repoFile, err = allFiles.Next()
		if err != nil {
			// Break at end of list
			if err == io.EOF {
				err = nil
				return
			}

			// Fail if next file doesnt work
			err = fmt.Errorf("failed retrieving commit file: %v", err)
			return
		}

		// Get file path
		repoFilePath := repoFile.Name

		printMessage(VerbosityData, "  Filtering file %s\n", repoFilePath)

		// Ensure file is valid against config
		commitHost, SkipFile := validateRepoFile(repoFilePath, deployerEndpoints)
		if SkipFile {
			// Not valid, skip
			continue
		}

		// Skip file if not user requested file (if requested)
		skipFile := checkForOverride(fileOverride, repoFilePath)
		if skipFile {
			printMessage(VerbosityFullData, "    File not desired\n")
			continue
		}

		printMessage(VerbosityData, "    File available\n")

		// Add repo file to the commit map with always create action
		commitFiles[repoFilePath] = "create"

		// If its a symlink - find target and add
		fileMode := fmt.Sprintf("%v", repoFile.Mode)
		fileType := determineFileType(fileMode)
		if fileType == "symlink" {
			// Get the target path of the sym link target and ensure it is valid
			var targetPath string
			targetPath, err = ResolveLinkToTarget(repoFilePath)
			if err != nil {
				err = fmt.Errorf("failed to parse symbolic link in commit: %v", err)
				return
			}

			// Add new action to this file that includes the expected target path for the link
			commitFiles[repoFilePath] = "symlinkcreate to target " + targetPath
		}

		// Add host to the map
		commitHosts[commitHost] = struct{}{}
	}

	return
}

// Filters files down to their associated host
// Also deduplicates and creates array of all relevant file paths for the deployment
func filterHostsAndFiles(tree *object.Tree, commitFiles map[string]string, commitHosts map[string]struct{}, DeployerEndpoints map[string]DeployerEndpoints, hostOverride string, SSHClientDefault SSHClientDefaults) (hostsAndEndpointInfo map[string]EndpointInfo, allDeploymentFiles map[string]string, err error) {
	// Show progress to user
	printMessage(VerbosityStandard, "Filtering deployment hosts... \n")

	// Get maps of all repo files for universal deduplication
	allHostsFiles, universalFiles, universalGroupFiles, err := mapAllRepoFiles(tree)
	if err != nil {
		return
	}

	// Initialize map
	hostsAndEndpointInfo = make(map[string]EndpointInfo) // Map of hosts and their associated endpoint information
	allDeploymentFiles = make(map[string]string)         // Map of all (filtered) deployment files and their associated actions

	printMessage(VerbosityProgress, "Creating files per host and all deployment files maps\n")

	// Loop hosts in config and prepare endpoint information and relevant configs for deployment
	for endpointName, endpointInfo := range DeployerEndpoints {
		printMessage(VerbosityData, "  Host %s: Filtering files...\n", endpointName)
		// Skip this host if not in override (if override was requested)
		skipHost := checkForOverride(hostOverride, endpointName)
		if skipHost {
			printMessage(VerbosityFullData, "    Host not desired\n")
			continue
		}

		// Check if host state is marked as offline, if so, skip this host
		if endpointInfo.HostState == "offline" {
			printMessage(VerbosityFullData, "    Host is marked as offline, skipping\n")
			continue
		}

		// Record universal files that are NOT to be used for this host (host has an override file)
		deniedUniversalFiles := findDeniedUniversalFiles(endpointName, allHostsFiles[endpointName], universalFiles, universalGroupFiles)

		// Filter committed files to their specific host and deduplicate against universal directory
		var filteredCommitFiles []string
		for commitFile, commitFileAction := range commitFiles {
			printMessage(VerbosityData, "    Filtering file %s\n", commitFile)

			// Split out the host part of the committed file path
			HostAndPath := strings.SplitN(commitFile, OSPathSeparator, 2)
			commitHost := HostAndPath[0]

			// Format a commitFilePath with the expected remote host path separators
			filePath := strings.ReplaceAll(commitFile, OSPathSeparator, "/")

			// Skip files not relevant to this host (either file is local to host, in global universal dir, or in host group universal)
			_, fileIsPartOfGroup := UniversalGroups[commitHost]
			if commitHost != endpointName && commitHost != UniversalDirectory && !fileIsPartOfGroup {
				printMessage(VerbosityFullData, "        File not for this host or not universal\n")
				continue
			}

			// Skip Universal Group files if host is not part of that group
			if fileIsPartOfGroup {
				var hostIsNotInUniversalGroup bool

				// Search through array of hosts in this files universal group
				for _, host := range UniversalGroups[commitHost] {
					if endpointName == host {
						// Host is part of the universal group
						hostIsNotInUniversalGroup = false
						break
					}
					hostIsNotInUniversalGroup = true
				}

				if hostIsNotInUniversalGroup {
					// Host is not part of universal group - skip file
					printMessage(VerbosityFullData, "        File is in Universal group and host is not in group\n")
					continue
				}
			}

			// Skip Universal files if this host ignores universal configs
			if endpointInfo.IgnoreUniversalConfs && commitHost == UniversalDirectory {
				printMessage(VerbosityFullData, "        File is universal and universal not requested for this host\n")
				continue
			}

			// Skip if commitFile is a universal file that is not allowed for this host
			_, fileIsDenied := deniedUniversalFiles[commitFile]
			if fileIsDenied {
				printMessage(VerbosityFullData, "        File is universal and host has non-universal identical file\n")
				continue
			}

			printMessage(VerbosityData, "        Selected\n")

			// Add file to the host-specific file list and the global deployment file map
			allDeploymentFiles[filePath] = commitFileAction
			filteredCommitFiles = append(filteredCommitFiles, filePath)
		}

		// Skip this host if no files to deploy
		if len(filteredCommitFiles) == 0 {
			continue
		}

		printMessage(VerbosityData, "    Retrieving endpoint options\n")

		// Parse out endpoint info and/or default SSH options
		var newInfo EndpointInfo
		newInfo, err = retrieveEndpointInfo(endpointInfo, SSHClientDefault)
		if err != nil {
			err = fmt.Errorf("failed to retrieve endpoint information: %v", err)
			return
		}

		// Write all deployment info for this host into the map
		newInfo.DeploymentFiles = filteredCommitFiles
		newInfo.EndpointName = endpointName
		hostsAndEndpointInfo[endpointName] = newInfo
	}

	return
}

// Retrieves all file content for this deployment
// Return vales provide the content keyed on local file path for the file data, metadata, hashes, and actions
func loadFiles(allDeploymentFiles map[string]string, tree *object.Tree) (commitFileInfo map[string]CommitFileInfo, err error) {
	// Show progress to user
	printMessage(VerbosityStandard, "Loading files for deployment... \n")

	// Initialize map of all local file paths and their associated info (content, metadata, hashes, and actions)
	commitFileInfo = make(map[string]CommitFileInfo)

	// Load file contents, metadata, hashes, and actions into their own maps
	for commitFilePath, commitFileAction := range allDeploymentFiles {
		printMessage(VerbosityData, "  Loading repository file %s\n", commitFilePath)

		// Ensure paths for deployment have correct separate for linux
		filePath := strings.ReplaceAll(commitFilePath, OSPathSeparator, "/")
		// As a reminder
		// filePath		should be identical to the full path of files in the repo except hard coded to forward slash path separators
		// commitFilePath	should be identical to the full path of files in the repo (meaning following the build OS file path separators)

		printMessage(VerbosityData, "    Repository file %s marked as 'to be %s'\n", commitFilePath, commitFileAction)

		// Skip loading if file will be deleted
		if commitFileAction == "delete" {
			// But, add it to the deploy target files so it can be deleted during ssh
			commitFileInfo[filePath] = CommitFileInfo{Action: commitFileAction}
			continue
		}

		// Skip loading if file is sym link
		if strings.Contains(commitFileAction, "symlinkcreate") {
			// But, add it to the deploy target files so it can be ln'd during ssh
			commitFileInfo[filePath] = CommitFileInfo{Action: commitFileAction}
			continue
		}

		// Skip loading other file types - safety blocker
		if commitFileAction != "create" {
			continue
		}

		printMessage(VerbosityData, "    Retrieving config file contents\n")

		// Get file from git tree
		var file *object.File
		file, err = tree.File(commitFilePath)
		if err != nil {
			err = fmt.Errorf("failed retrieving file from git tree: %v", err)
			return
		}

		// Open reader for file contents
		var reader io.ReadCloser
		reader, err = file.Reader()
		if err != nil {
			err = fmt.Errorf("failed retrieving file reader: %v", err)
			return
		}
		defer reader.Close()

		// Read file contents (as bytes)
		var content []byte
		content, err = io.ReadAll(reader)
		if err != nil {
			err = fmt.Errorf("failed reading file content: %v", err)
			return
		}

		printMessage(VerbosityData, "    Extracting config file metadata\n")

		// Grab metadata out of contents
		var metadata, configContent string
		metadata, configContent, err = extractMetadata(string(content))
		if err != nil {
			err = fmt.Errorf("failed to extract metadata header: %v", err)
			return
		}

		printMessage(VerbosityData, "    Hashing config content\n")

		// SHA256 Hash the metadata-less contents
		contentHash := SHA256Sum(configContent)

		printMessage(VerbosityData, "    Parsing metadata header JSON\n")

		// Parse JSON into a generic map
		var jsonMetadata MetaHeader
		err = json.Unmarshal([]byte(metadata), &jsonMetadata)
		if err != nil {
			err = fmt.Errorf("failed parsing JSON metadata header for %s: %v", commitFilePath, err)
			return
		}

		// Put all information gathered into struct
		var info CommitFileInfo
		info.FileOwnerGroup = jsonMetadata.TargetFileOwnerGroup
		info.FilePermissions = jsonMetadata.TargetFilePermissions
		info.ReloadRequired = jsonMetadata.ReloadRequired
		info.Reload = jsonMetadata.ReloadCommands
		info.Hash = contentHash
		info.Data = configContent
		info.Action = commitFileAction

		// Save info struct into map for this file
		commitFileInfo[filePath] = info
	}

	return
}

func getFailTrackerCommit() (commitID string, failTrackerPath string, failures []string, err error) {
	printMessage(VerbosityProgress, "Retrieving commit ID from failtracker file\n")

	// Regex to match commitid line from fail tracker
	failCommitRegEx := regexp.MustCompile(`commitid:([0-9a-fA-F]+)\n`)

	// Failure tracker file path
	failTrackerPath = filepath.Join(RepositoryPath, FailTrackerFile)

	// Read in contents of fail tracker file
	lastFailTrackerBytes, err := os.ReadFile(failTrackerPath)
	if err != nil {
		return
	}

	// Convert tracker to string
	lastFailTracker := string(lastFailTrackerBytes)

	// Use regex to extract commit hash from line in fail tracker (should be the first line)
	commitRegexMatches := failCommitRegEx.FindStringSubmatch(lastFailTracker)

	// Extract the commit hash hex from the failtracker
	if len(commitRegexMatches) < 2 {
		err = fmt.Errorf("commitid missing from failtracker file")
		return
	}
	commitID = commitRegexMatches[1]

	// Remove commit line from the failtracker contents using the commit regex
	lastFailTracker = failCommitRegEx.ReplaceAllString(lastFailTracker, "")

	// Put failtracker failures into array
	failures = strings.Split(lastFailTracker, "\n")

	return
}

// Reads in last failtracker file and retrieves individual failures and the commitHash of the failure
func getFailedFiles(failures []string, fileOverride string) (commitFiles map[string]string, commitHosts map[string]struct{}, err error) {
	// Initialize maps
	commitFiles = make(map[string]string)
	commitHosts = make(map[string]struct{})

	printMessage(VerbosityProgress, "Parsing failtracker lines\n")

	// Retrieve failed hosts and files from failtracker json by line
	for _, fail := range failures {
		// Skip any empty lines
		if fail == "" {
			continue
		}

		// Use global struct for errors json format
		var errorInfo ErrorInfo

		// Unmarshal the line into vars
		err = json.Unmarshal([]byte(fail), &errorInfo)
		if err != nil {
			err = fmt.Errorf("issue unmarshaling json: %v", err)
			return
		}

		// error if no hostname
		if errorInfo.EndpointName == "" {
			err = fmt.Errorf("hostname is empty: failtracker line: %s", fail)
			return
		}

		printMessage(VerbosityData, "Parsing failure for host %v\n", errorInfo.EndpointName)

		// Add failed hosts to isolate host deployment loop to only those hosts
		commitHosts[errorInfo.EndpointName] = struct{}{}

		// error if no files
		if len(errorInfo.Files) == 0 {
			err = fmt.Errorf("no files in failtracker line: %s", fail)
			return
		}

		// Add failed files to array (Only create, deleted/symlinks dont get added to failtracker)
		for _, failedFile := range errorInfo.Files {
			printMessage(VerbosityData, "Parsing failure for file %s\n", failedFile)

			// Skip this file if not in override (if override was requested)
			skipFile := checkForOverride(fileOverride, failedFile)
			if skipFile {
				continue
			}

			printMessage(VerbosityData, "Marked host %s - file %s for redeployment\n", errorInfo.EndpointName, failedFile)

			commitFiles[failedFile] = "create"
		}
	}

	return
}
