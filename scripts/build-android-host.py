#!/usr/bin/env python3
"""Build native extensions through NSGo's official AAR/embedding API.

Use the Android SDK to compile the manifest (including service declarations)
and sign with the existing project identity, supplied by CI for releases.
"""
import argparse, hashlib, os, pathlib, re, shutil, subprocess, tempfile, zipfile

root = pathlib.Path(__file__).resolve().parent.parent / 'clients/nsgo'
parser = argparse.ArgumentParser()
parser.add_argument('--output', default='dist')
parser.add_argument('--targets', required=True)
args = parser.parse_args()
out = pathlib.Path(args.output).resolve()
name = re.search(r'^name:\s*(\S+)\s*$', (root/'nsgo.yaml').read_text(), re.M).group(1)
version = os.environ.get('REIGNHUB_BUILD_VERSION', '')
build_number = os.environ.get('REIGNHUB_BUILD_NUMBER', '')
overrides = []
if version:
    overrides += ['-version', version]
if build_number:
    overrides += ['-build-number', build_number]
android = [t for t in args.targets.split(',') if t.startswith('android_')]
if not android:
    raise SystemExit(0)
sdk = pathlib.Path(os.environ.get('ANDROID_HOME', str(pathlib.Path.home()/'Library/Android/sdk')))
platforms = sorted(sdk.glob('platforms/android-*/android.jar'))
d8s = sorted(sdk.glob('build-tools/*/d8'))
if not platforms or not d8s or not shutil.which('javac'):
    raise SystemExit('Native Android host requires JDK 17+ and an Android SDK (ANDROID_HOME).')
for target in android:
    with tempfile.TemporaryDirectory(prefix='reignhub-native-android-') as tmp:
        tmp = pathlib.Path(tmp)
        subprocess.run(['go','tool','nsgo','build','-embed','-target',target,'-output',str(tmp/'aar'),*overrides],cwd=root,check=True)
        aar = next((tmp/'aar').rglob(name+'.aar'))
        with zipfile.ZipFile(aar) as z:
            (tmp/'classes.jar').write_bytes(z.read('classes.jar'))
        classes = tmp/'classes'; classes.mkdir()
        sources = list((root/'native/android').rglob('*.java'))
        jar = str(platforms[-1])
        subprocess.run(['javac','-nowarn','--release','17','-classpath',os.pathsep.join([jar,str(tmp/'classes.jar')]),'-d',str(classes),*map(str,sources)],check=True)
        subprocess.run([str(d8s[-1]),'--lib',jar,'--min-api','28','--output',str(tmp),str(tmp/'classes.jar'),*map(str,classes.rglob('*.class'))],check=True)
        matches = list(out.glob(f'{name}-{version or "*"}-{target}/{name}.apk'))
        if len(matches) != 1:
            raise SystemExit(f'Expected one {target} APK in {out}, found {len(matches)}')
        apk = matches[0]
        patched = tmp/(name+'.apk')
        # aapt2 supports Android's full manifest schema. The lightweight NSGo
        # encoder intentionally only supports its standalone activity fields.
        manifest = tmp/'AndroidManifest.xml'
        subprocess.run(['go','run','./cmd/android-package','-sdk-manifest',str(manifest),*overrides],cwd=root,check=True)
        tools = d8s[-1].parent
        icon = tmp/'res/mipmap-xxxhdpi/ic_launcher.png'
        resource_inputs = []
        with zipfile.ZipFile(apk) as z:
            if 'res/mipmap-xxxhdpi-v4/ic_launcher.png' in z.namelist():
                icon.parent.mkdir(parents=True)
                icon.write_bytes(z.read('res/mipmap-xxxhdpi-v4/ic_launcher.png'))
                manifest.write_text(manifest.read_text().replace('<application', '<application android:icon="@mipmap/ic_launcher"', 1))
                resources = tmp/'resources.zip'
                subprocess.run([str(tools/'aapt2'),'compile',str(icon),'-o',str(resources)],check=True)
                resource_inputs = [str(resources)]
        linked = tmp/'manifest.apk'
        subprocess.run([str(tools/'aapt2'),'link','-I',jar,'--manifest',str(manifest),*resource_inputs,'-o',str(linked)],check=True)
        unsigned = tmp/'unsigned.apk'
        with zipfile.ZipFile(apk) as old, zipfile.ZipFile(linked) as native, zipfile.ZipFile(unsigned,'w') as dest:
            for entry in old.infolist():
                if entry.filename in ('AndroidManifest.xml','resources.arsc') or entry.filename.startswith(('META-INF/','classes','res/')):
                    continue
                dest.writestr(entry,old.read(entry))
            for entry in native.infolist():
                binary = native.read(entry)
                entry.compress_type = zipfile.ZIP_STORED
                dest.writestr(entry,binary)
            dest.write(tmp/'classes.dex','classes.dex',compress_type=zipfile.ZIP_DEFLATED)
        aligned = tmp/'aligned.apk'
        subprocess.run([str(tools/'zipalign'),'-p','-f','4',str(unsigned),str(aligned)],check=True)
        identity = root/'.nsgo/android/debug.pem'
        key,cert = tmp/'signing-key.der',tmp/'signing-cert.pem'
        subprocess.run(['openssl','pkcs8','-topk8','-nocrypt','-in',str(identity),'-outform','DER','-out',str(key)],check=True)
        subprocess.run(['openssl','x509','-in',str(identity),'-out',str(cert)],check=True)
        key.chmod(0o600)
        subprocess.run([str(tools/'apksigner'),'sign','--key',str(key),'--cert',str(cert),'--out',str(patched),str(aligned)],check=True)
        subprocess.run([str(tools/'apksigner'),'verify',str(patched)],check=True)
        shutil.copy2(patched,apk)
        # NSGo names arm64 => android-arm64, x64 => android-x64 in releases.
        release = list((out/'releases').glob(f'{name.lower()}-{version or "*"}-{target.replace("_","-")}.apk'))
        for dest in release:
            shutil.copy2(patched,dest)
            dest.with_name(dest.name+'.sha256').write_text(hashlib.sha256(dest.read_bytes()).hexdigest()+'  '+dest.name+'\n')
