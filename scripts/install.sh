#!/usr/bin/env sh
# This script is used in the README and https://www.infracost.io/docs/#quick-start
set -e

# check_sha is separated into a defined function so that we can
# capture the exit code effectively with `set -e` enabled
check_sha() {
  (
    cd /tmp/
    shasum -sc "$1"
  )

  return $?
}

os=$(uname | tr '[:upper:]' '[:lower:]')
arch=$(uname -m | tr '[:upper:]' '[:lower:]' | sed -e s/x86_64/amd64/)
if [ "$arch" = "aarch64" ]; then
  arch="arm64"
fi

version=${INFRACOST_VERSION:-latest}

# This script only installs versions >=2.0.0; older versions are at https://github.com/infracost/infracost.
if [ "$version" != "latest" ]; then
  major=$(echo "$version" | sed 's/^v//' | cut -d. -f1)
  if ! echo "$major" | grep -qE '^[0-9]+$'; then
    echo "Error: invalid version format: $version"
    exit 1
  fi
  if [ "$major" -lt 2 ]; then
    echo "Error: version $version is not supported by this script."
    echo "This script only installs versions >=2.0.0 from the infracost/cli repository."
    echo "For older versions, see https://github.com/infracost/infracost."
    exit 1
  fi
fi

url="https://infracost.io/downloads/${version}"
tar="infracost-$os-$arch.tar.gz"
echo "Downloading version ${version} of infracost-$os-$arch..."
curl -sL "$url/$tar" -o "/tmp/$tar"
echo

code=$(curl -s -L -o /dev/null -w "%{http_code}" "$url/$tar.sha256")
if [ "$code" = "404" ]; then
    echo "Skipping checksum validation as the sha for the release could not be found, no action needed."
else
  if [ -x "$(command -v shasum)" ]; then
    echo "Validating checksum for infracost-$os-$arch..."
    curl -sL "$url/$tar.sha256" -o "/tmp/$tar.sha256"

    if ! check_sha "$tar.sha256"; then
      echo
      read -r -p "Installation checksum failed. This could be a security issue. Would you like to continue? (y/n) " answer
      if [ "$answer" != "y" ]; then
        echo
        echo "Exiting, please email hello@infracost.io for help."
        exit 1
      fi
    fi

    rm "/tmp/$tar.sha256"
  else
    echo "Skipping checksum validation as the shasum command could not be found, no action needed."
  fi
fi
echo

tar xzf "/tmp/$tar" -C /tmp
rm "/tmp/$tar"

install_dir="/usr/local/bin"
local_bin=""
if [ -n "$HOME" ]; then
  local_bin="$HOME/.local/bin"
  case ":$PATH:" in
    *":$local_bin:"*) install_dir="$local_bin" ;;
  esac
fi

if [ ! -d "$install_dir" ]; then
  if [ "$install_dir" = "$local_bin" ]; then
    mkdir -p "$install_dir"
  elif [ -x "$(command -v sudo)" ]; then
    sudo mkdir -p "$install_dir"
  else
    mkdir -p "$install_dir"
  fi
fi

echo "Moving /tmp/infracost to $install_dir/infracost"
if [ -w "$install_dir" ]; then
  mv "/tmp/infracost" "$install_dir/infracost"
elif [ -x "$(command -v sudo)" ]; then
  echo "You might be asked for your password due to sudo."
  sudo mv "/tmp/infracost" "$install_dir/infracost"
else
  mv "/tmp/infracost" "$install_dir/infracost"
fi
echo
echo "Completed installing $($install_dir/infracost --version)"
