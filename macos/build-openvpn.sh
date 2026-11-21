#!/bin/sh
# Builds a universal OpenVPN (arm64 + x86_64), statically linked against
# OpenSSL, lz4 and lzo, into build/openvpn. One file, no dylibs, so the
# daemon can pin a single sha256 (see tunnels.go). Sources are downloaded
# once and checked against the hashes below.
#
# The OpenSSL hash is the one OpenSSL publishes (openssl-3.5.9.tar.gz.sha256).
# The OpenVPN tarball's signature was checked with the OpenVPN release key
# (F554 A368 7412 CFFE BDEF E0A3 12F5 F7B4 2F2B 01E7). lz4 and lzo hashes
# were recorded when first downloaded from their official release pages.
#
# OpenVPN is GPL-2.0 (with an OpenSSL exception); we only run it as a separate
# program. build-app.sh puts its license texts in the app (Resources/Licenses).
set -eu
cd "$(dirname "$0")"

OPENSSL=3.5.9;  OPENSSL_SHA=603f5602e2eef00d77fbd429d34dcd5822bb301757a1bc9cdb24c670f1eb859a
OPENVPN=2.6.23; OPENVPN_SHA=4041c709162bec1325abf5aa8cf27a255cc477c634b15ee310411c701fc40a96
LZ4=1.11.1;     LZ4_SHA=683fed08a844ad012196381e40badf80c0bbe4962959761416d41c9e67d77ec7
LZO=2.10;       LZO_SHA=c0f892943208266f9b6543b3ae308fab6284c5c90e627931446fb49b4221a072

WORK=.build/openvpn
OUT=.build/openvpn/openvpn-universal # build-app.sh copies it into the app
MIN=13.0
STAMP="$OPENSSL $OPENVPN $LZ4 $LZO $MIN"
mkdir -p "$WORK/src"

if [ -f "$OUT" ] && [ "$(cat "$WORK/stamp" 2>/dev/null)" = "$STAMP" ]; then
  echo "$OUT (cached)"; exit 0
fi

fetch() { # name url sha
  f="$WORK/src/$1"
  [ -f "$f" ] || curl -fsSL -o "$f" "$2"
  echo "$3  $f" | shasum -a 256 -c - >/dev/null || { echo "bad checksum: $1" >&2; rm -f "$f"; exit 1; }
}
fetch "openssl-$OPENSSL.tar.gz" "https://github.com/openssl/openssl/releases/download/openssl-$OPENSSL/openssl-$OPENSSL.tar.gz" "$OPENSSL_SHA"
fetch "openvpn-$OPENVPN.tar.gz" "https://build.openvpn.net/downloads/releases/openvpn-$OPENVPN.tar.gz" "$OPENVPN_SHA"
fetch "lz4-$LZ4.tar.gz" "https://github.com/lz4/lz4/releases/download/v$LZ4/lz4-$LZ4.tar.gz" "$LZ4_SHA"
fetch "lzo-$LZO.tar.gz" "https://www.oberhumer.com/opensource/lzo/download/lzo-$LZO.tar.gz" "$LZO_SHA"

JOBS=$(sysctl -n hw.ncpu)

build_arch() { # arch(arm64|x86_64) openssl-target host
  arch=$1; target=$2; host=$3
  P="$PWD/$WORK/$arch"
  rm -rf "$P" "$WORK/b-$arch"; mkdir -p "$P" "$WORK/b-$arch"
  CFLAGS="-arch $arch -mmacosx-version-min=$MIN -O2"
  export CC="clang -arch $arch -mmacosx-version-min=$MIN"
  B="$WORK/b-$arch"

  tar -xzf "$WORK/src/openssl-$OPENSSL.tar.gz" -C "$B"
  (cd "$B/openssl-$OPENSSL" && ./Configure "$target" no-shared no-tests no-apps no-docs "-mmacosx-version-min=$MIN" --prefix="$P" --libdir=lib >/dev/null \
    && make -j"$JOBS" build_libs >/dev/null && make install_dev >/dev/null)

  tar -xzf "$WORK/src/lz4-$LZ4.tar.gz" -C "$B"
  (cd "$B/lz4-$LZ4" && make -C lib -j"$JOBS" liblz4.a CC="$CC" CFLAGS="$CFLAGS" >/dev/null \
    && mkdir -p "$P/lib" "$P/include" && cp lib/liblz4.a "$P/lib/" && cp lib/lz4.h lib/lz4hc.h "$P/include/")

  tar -xzf "$WORK/src/lzo-$LZO.tar.gz" -C "$B"
  (cd "$B/lzo-$LZO" && ./configure --host="$host" --disable-shared --enable-static --prefix="$P" CFLAGS="$CFLAGS" >/dev/null \
    && make -j"$JOBS" >/dev/null && make install >/dev/null)

  tar -xzf "$WORK/src/openvpn-$OPENVPN.tar.gz" -C "$B"
  (cd "$B/openvpn-$OPENVPN" && ./configure --host="$host" --disable-shared --enable-static --disable-plugins \
      --disable-pkcs11 --disable-debug --disable-systemd --disable-dco --disable-unit-tests \
      --with-crypto-library=openssl --enable-lz4 --enable-lzo \
      OPENSSL_CFLAGS="-I$P/include" OPENSSL_LIBS="-L$P/lib -lssl -lcrypto" \
      LZ4_CFLAGS="-I$P/include" LZ4_LIBS="-L$P/lib -llz4" \
      LZO_CFLAGS="-I$P/include" LZO_LIBS="-L$P/lib -llzo2" \
      CFLAGS="$CFLAGS" LDFLAGS="-arch $arch -mmacosx-version-min=$MIN" >/dev/null \
    && make -j"$JOBS" >/dev/null && cp src/openvpn/openvpn "$P/openvpn")
  unset CC
}

build_arch arm64 darwin64-arm64-cc aarch64-apple-darwin
build_arch x86_64 darwin64-x86_64-cc x86_64-apple-darwin
lipo -create "$WORK/arm64/openvpn" "$WORK/x86_64/openvpn" -output "$OUT"
strip -x "$OUT"
# A dylib dependency would break the single-file pin: only system libraries allowed.
# (Dependency lines start with a tab; the per-architecture headers don't.)
BAD=$(otool -L "$OUT" | awk '/^\t/ && $1 !~ /^\/(usr\/lib|System)\//')
if [ -n "$BAD" ]; then
  echo "openvpn links a non-system library:" >&2; echo "$BAD" >&2; exit 1
fi
echo "$STAMP" > "$WORK/stamp"
echo "$OUT"
