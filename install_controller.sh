#!/bin/bash
#### Error handling

logError() {
	local errorMessage=$1
	local exitRequested=$2

	echo "[-] Error: $errorMessage"
	if $exitRequested == "true"
	then
		exit 1
	fi
}

#### Pre Checks

# Ensure script is run in bash
if [ -z "$BASH_VERSION" ]
then
	logError "This script must be run in Bash." "true"
fi

# Check for commands
command -v git >/dev/null || logError "git command not found." "true"
command -v egrep >/dev/null || logError "egrep command not found." "true"
command -v sed >/dev/null || logError "sed command not found." "true"
command -v ssh >/dev/null || logError "ssh command not found." "true"
command -v dirname >/dev/null || logError "dirname command not found." "true"
command -v mkdir >/dev/null || logError "mkdir command not found." "true"
command -v echo >/dev/null || logError "echo command not found." "true"
command -v mv >/dev/null || logError "mv command not found." "true"
command -v rm >/dev/null || logError "rm command not found." "true"
command -v cat >/dev/null || logError "cat command not found." "true"
command -v chmod >/dev/null || logError "chmod command not found." "true"
command -v ls >/dev/null || logError "ls command not found." "true"
command -v tr >/dev/null || logError "tr command not found." "true"
command -v ssh-keygen >/dev/null || logError "ssh-keygen command not found." "true"

#### Installation

echo "     SCMPusher Controller Installer     "
echo "========================================"

read -p "Press enter to begin the installation"
echo "========================================"

# Default choices
executablePath="/usr/local/bin/scmcontroller"
RepositoryPath="~/SCMGit"
BranchName="main"
configFilePath="$RepositoryPath/scmpc.yaml"
LogJournalBool="false"
TemplateDirectory="goldentemplate"
SSHIdentityPath="~/.ssh/scmp_ssh.key"
SSHPort="2022"
MaximumOutboundConnections="10"
SudoPassword=""

#### User Choices
echo -e "Provide your choices for the installation. Press enter for the default.\n"

# Exec Path
echo "[*] Enter the full path and file name where you would like the controller executable to be"
read -e -p "    (Default '$executablePath'): " UserChoice_executablePath
if [[ $UserChoice_executablePath != "" ]]
then
	executablePath=$UserChoice_executablePath
fi

# Repo Path
echo "[*] Enter the path in which you would like a new repository to be created"
read -e -p "    (Default '$RepositoryPath'): " UserChoice_RepositoryPath
if [[ $UserChoice_RepositoryPath != "" ]]
then
	RepositoryPath=$UserChoice_RepositoryPath
	# Override default config with new parent dir
	configFilePath="$RepositoryPath/scmpc.yaml"
fi

# Config Path
echo "[*] Enter the full path and file name where you would like the controller config to be"
read -e -p "    (Default '$configFilePath'): " UserChoice_configFilePath
if [[ $UserChoice_configFilePath != "" ]]
then
	configFilePath=$UserChoice_configFilePath
fi

## Config user choices
# ssh key path
echo "[*] Enter the full path and file name to the SSH private key for the controller (ed25519 key type required)"
read -e -p "    (Default '$SSHIdentityPath'): " UserChoice_SSHIdentityPath
if [[ $UserChoice_SSHIdentityPath != "" ]]
then
	SSHIdentityPath=$UserChoice_SSHIdentityPath
	# user supplied only if the key exists
	if [[ $(ls $SSHIdentityPath 2>&1 1>/dev/null) ]]
	then
		UserSuppliedKey="true"
	else
		UserSuppliedKey="false"
	fi
fi

# Max outbound connections at once
echo "[*] Enter the maximum number of allowed outbound connections for the controller"
read -e -p "    (Default '$MaximumOutboundConnections'): " UserChoice_MaximumOutboundConnections
if [[ $UserChoice_MaximumOutboundConnections != "" ]]
then
	MaximumOutboundConnections=$UserChoice_MaximumOutboundConnections
fi

# SSH User password
echo "[*] Enter the SSH password for the user of all remote hosts (this will be used for sudo only, leave blank for unrestricted sudo access)"
read -e -s -p "    Password: " UserChoice_SudoPassword
if [[ $UserChoice_SudoPassword != "" ]]
then
	read -e -s -p "    Password again: " UserChoice_SudoPasswordConfirm
	if [[ $UserChoice_SudoPassword == $UserChoice_SudoPasswordConfirm ]]
	then
		SudoPassword=$UserChoice_SudoPassword
	else
		logError "Passwords do not match, try again." "true"
	fi
fi

echo "[*] Enter the name of the initial branch for the repository"
read -e -p "    (Default '$BranchName'): " UserChoice_BranchName
if [[ $UserChoice_BranchName != "" ]]
then
	BranchName=$UserChoice_BranchName
fi

echo "[*] Enter the name of the template directory inside the repository"
read -e -p "    (Default '$TemplateDirectory'): " UserChoice_TemplateDirectory
if [[ $UserChoice_TemplateDirectory != "" ]]
then
	TemplateDirectory=$UserChoice_TemplateDirectory
fi

echo "[*] Add all the SSH hosts in your SSH config file to the deployer endpoints?"
read -e -p "    [y\N]: " AddSSHConfigHostsConfirmation
AddSSHConfigHostsConfirmation=$(echo $AddSSHConfigHostsConfirmation | tr [:upper:] [:lower:])

echo "[*] Will you be using the Deployer SSH server instead of the standard SSH server?"
read -e -p "    [y/N]: " RemoteUsesDeployerServer
RemoteUsesDeployerServer=$(echo $RemoteUsesDeployerServer | tr [:upper:] [:lower:])

echo "[*] Add example metadata headers to repository?"
read -e -p "    [y\N]: " CreateExamplesConfirmation
CreateExamplesConfirmation=$(echo $CreateExamplesConfirmation | tr [:upper:] [:lower:])

echo "[*] Install git post-commit hook? (REQUIRED if operating controller in auto-deploy mode)"
read -e -p "    [y\N]: " InstallHookConfirmation
InstallHookConfirmation=$(echo $InstallHookConfirmation | tr [:upper:] [:lower:])

echo "[*] Log controller errors to journald?"
read -e -p "    [y/N]: " LogJournalBoolConfirmation
LogJournalBoolConfirmation=$(echo $LogJournalBoolConfirmation | tr [:upper:] [:lower:])

echo "[*] Use your SSH agent to retrieve private keys?"
read -e -p "    [y/N]: " SSHAgentBoolConfirmation
SSHAgentBoolConfirmation=$(echo $SSHAgentBoolConfirmation | tr [:upper:] [:lower:])

# Ask for confirmation before continuing
echo "[*] Are the answers above all correct? Enter 'n' or nothing to exit"
read -e -p "    [y/N]: " ChoicesConfirmation
if [[ $ChoicesConfirmation != "y" ]]
then
	logError "aborting installation" "true"
fi

#### Actions on choices

# Quit if repo already exists
if [[ ! $(ls -ld $RepositoryPath 2>&1 1>/dev/null) ]]
then
	logError "Repository at $RepositoryPath already exists, not creating new repo." "true"
fi

# Put executable from local dir in user choosen location
PAYLOAD_LINE=$(awk '/^__PAYLOAD_BEGINS__/ { print NR + 1; exit 0; }' $0)
executableDirs=$(dirname $executablePath 2>/dev/null || logError "failed to determine executable parent directories" "true")
mkdir -p $executableDirs 2>/dev/null || logError "failed to create executable parent directory" "true"
tail -n +${PAYLOAD_LINE} $0 | base64 -d | tar -zpvx -C $executableDirs || logError "failed to extract embedded executable" "true"
mv $executableDirs/controller $executablePath 2>/dev/null || logError "failed to move executable" "true"
echo "[+] Successfully extracted deployer binary to $executablePath"

# Create git repo
mkdir -p $RepositoryPath 2>/dev/null || logError "failed to create repository parent directories" "true"
cd $RepositoryPath
git init -q --initial-branch=$BranchName || logError "failed to initialize git repository" "true"
echo "[+] Successfully created git repository in '$RepositoryPath'"

# create template dir
mkdir -p $RepositoryPath/$TemplateDirectory 2>/dev/null || logError "failed to create template directory" "true"
echo "[+] Successfully created Template Directory at '$RepositoryPath/$TemplateDirectory'"

# SSH key generation
if [[ $UserSuppliedKey != "true" ]]
then
	ssh-keygen -t ed25519 -N '' -C scmp/controller -f $SSHIdentityPath || logError "failed to generate private key" "true"
	SSHPublicKey=$(cat $SSHIdentityPath.pub)
	rm $SSHIdentityPath.pub
	echo "[+] Successfully created new SSH private key in $SSHIdentityPath"
elif [[ $UserSuppliedKey == "true" ]]
then
	SSHPublicKey=$(ssh-keygen -y -f $SSHIdentityPath) || logError "invalid ssh key in $SSHIdentityPath" "true"
	echo "[+] Validated supplied SSH key at $SSHIdentityPath"
fi

# Journald log choice
if [[ $LogJournalBoolConfirmation == "y" ]]
then
	LogJournalBool="true"
else
	LogJournalBool="false"
fi

# SSH Agent choice
if [[ $SSHAgentBoolConfirmation == "y" ]]
then
	SSHAgentBool="true"
else
	SSHAgentBool="false"
fi

# Put config in user choosen location
echo "Controller:
  # Path to the root of the git repository
  RepositoryPath: "$repositoryPath"
  LogtoJournald: $LogJournalBool
  SSHClient:
    # File path for client's SSH key
    SSHIdentityFile: "$SSHIdentityPath"
    # Set to true if you want to use your SSH agent to retrieve the private key (requires pubkey in identity file)
    UseSSHAgent: $SSHAgentBool
    # Limit number of ssh outbound connections at once
    MaximumConnectionsAtOnce: $MaximumOutboundConnections
    # Password that will be used to run sudo commands on remote host
    # Leave blank if sudo does not require a password
    SudoPassword: "$SudoPassword"
# Repo dir to house all configs that should be deployed to every host
TemplateDirectory: "$TemplateDirectory"
# Remote hosts to receive configurations
DeployerEndpoints:
  # name of each endpoint must have a matching directory name in the root of the git repo
  #examplehost:
  #  - endpoint: 127.0.0.1
  #  - endpointPort: 2022
  #  - endpointUser: "deployer"" > $configFilePath || logError "failed to write configuration to %configFilePath" "true"
echo "[+] Successfully created controller configuration  in '$configFilePath'"

if [[ $AddSSHConfigHostsConfirmation == "y" ]]
then
	sshConfigHosts=$(egrep "^Host " ~/.ssh/config | sed 's/Host //g')
	for line in $sshConfigHosts
	do
		endpointName=$line
		endpoint=$(ssh -G $endpointName | egrep "^hostname " | sed 's/hostname //g')
		endpointPort=$(ssh -G $endpointName | egrep "^port "  | sed 's/port //g')
		endpointUser=$(ssh -G $endpointName | egrep "^user " | sed 's/user //g')
		if [[ $RemoteUsesDeployerServer == y ]]
		then
			endpointPort="2022"
			endpointUser="deployer"
		fi
		echo "  $endpointName:
    - endpoint: "$endpoint"
    - endpointPort: $endpointPort
    - endpointUser: "$endpointUser"" >> $configFilePath
		mkdir $RepositoryPath/$endpointName 2>/dev/null || logError "failed to create host $endpointName directory in git repository" "false"
	done
	echo "[+] Successfully added SSH client config hosts to '$configFilePath'"
fi

# Add metadata example files
if [[ $CreateExamplesConfirmation == "y" ]]
then
	echo "#|^^^|#
{
  "FileOwnerGroup": "root:root",
  "FilePermissions": 644,
  "ReloadRequired": true,
  "Reload": [
    "systemctl restart systemd-timesyncd",
    "systemctl is-active systemd-timesyncd"
  ]
}
#|^^^|#" > $RepositoryPath/.example-metadata-header.txt

	echo "#|^^^|#
{
  "FileOwnerGroup": "root:root",
  "FilePermissions": 644,
  "ReloadRequired": false
}
#|^^^|#" > $RepositoryPath/.example-metadata-header-noreload.txt
	chmod 640 $RepositoryPath/.example-metadata-header.txt || logError "failed to change permissions for example metadata file" "false"
	chmod 640 $RepositoryPath/.example-metadata-header-noreload.txt || logError "failed to change permissions for example metadata file" "false"
fi

# Create first commit
GIT_AUTHOR_EMAIL=""
GIT_COMMITTER_EMAIL=""
git add . || logError "failed to git add, please fix error, disable hook, git add and commit" "false"
git commit -m 'Initial Automated Commit' --author 'SCMPController <scmpc@localhost>' || logError "failed to git commit, please fix error, disable hook, and re-commit" "false"
echo "[+] Successfully created an initial commit to new repository"

# Install git hook script
if [[ $InstallHookConfirmation == "y" ]]
then
	echo "#!/bin/bash
$executablePath --auto-deploy -c $configFilePath
" > $RepositoryPath/.git/hooks/post-commit || logError "failed to write post-commit hook to git repository" "true"
	chmod 750 $RepositoryPath/.git/hooks/post-commit
	echo "[+] Successfully created git post-commit hook in git repository"
fi

echo "[+] New git repository created in $RepositoryPath with initial branch $BranchName and template directory $TemplateDirectory"
echo "  [*] Don't forget to add the public key to all of your deployer endpoints:\n    $SSHPublicKey"

exit 0

# Controller Binary Embed #
__PAYLOAD_BEGINS__
