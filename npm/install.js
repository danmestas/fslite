#!/usr/bin/env node
//
// fslite npm install — builds the Go binary into ./bin/fslite via
// `go install`. Requires Go 1.21+ on the user's machine.
//
// Plan for v0.2: drop the Go-toolchain requirement by publishing
// pre-built binaries as platform-specific npm packages (esbuild-style),
// driven by a GitHub Actions release workflow. Until then this is the
// simplest cross-platform install path that doesn't lock us into a
// release pipeline before there's even a release.

'use strict';

const { execSync } = require('child_process');
const fs = require('fs');
const path = require('path');
const os = require('os');

const pkg = require('./package.json');
const goPackage = pkg.config.fslite_go_package;

function die(msg) {
  console.error('fslite postinstall: ' + msg);
  process.exit(1);
}

// 1. Verify Go is on PATH.
let goVersion = '';
try {
  goVersion = execSync('go version', { stdio: ['pipe', 'pipe', 'pipe'] })
    .toString()
    .trim();
} catch {
  die(
    'Go 1.21+ is required.\n' +
      '  Install from https://go.dev/dl/ and re-run `npm install fslite`.\n' +
      '  (Future versions will ship prebuilt binaries — track ' +
      'https://github.com/danmestas/fslite/issues for progress.)'
  );
}
console.log('fslite: using ' + goVersion);

// 2. Pick the GOBIN we'll install into. Default to a vendored
//    location inside the package so npm uninstall cleans up.
const localGobin = path.join(__dirname, 'bin');
if (!fs.existsSync(localGobin)) {
  fs.mkdirSync(localGobin, { recursive: true });
}

// 3. Run `go install` with our local GOBIN.
const binaryName = os.platform() === 'win32' ? 'fslite.exe' : 'fslite';
const binaryPath = path.join(localGobin, binaryName);

console.log('fslite: installing ' + goPackage + ' → ' + binaryPath);
try {
  execSync('go install ' + goPackage, {
    stdio: 'inherit',
    env: Object.assign({}, process.env, { GOBIN: localGobin }),
  });
} catch (err) {
  die('go install failed: ' + err.message);
}

if (!fs.existsSync(binaryPath)) {
  die('binary not produced at ' + binaryPath + ' (this is a packaging bug)');
}

console.log('fslite: installed. Test with `fslite --help`.');
