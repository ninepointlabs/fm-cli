#!/bin/bash
set -e

VERSION="0.2.4"
ARCH=$(uname -m)
case $ARCH in
    x86_64) RPM_ARCH="x86_64" ;;
    aarch64) RPM_ARCH="aarch64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "Building fm-cli v${VERSION} for ${RPM_ARCH}..."

# Create build directories
mkdir -p ~/rpmbuild/{BUILD,RPMS,SOURCES,SPECS,SRPMS}

# Create tarball
TARBALL="fm-cli-${VERSION}"
mkdir -p "${TARBALL}"
cp -r cmd internal go.mod go.sum README.md LICENSE "${TARBALL}/" 2>/dev/null || cp -r cmd internal go.mod go.sum README.md "${TARBALL}/"
tar czf ~/rpmbuild/SOURCES/${TARBALL}.tar.gz "${TARBALL}"
rm -rf "${TARBALL}"

# Copy spec file
cp packaging/rpm/fm-cli.spec ~/rpmbuild/SPECS/

# Build RPM
rpmbuild -ba ~/rpmbuild/SPECS/fm-cli.spec

echo "Package created in ~/rpmbuild/RPMS/${RPM_ARCH}/"
