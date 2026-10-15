"""Self-contained real selective-build regression; no workspace or optional modules."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
CORE = 'github.com/RussellLuo/qi/kernel'
PLUGIN = 'example.com/qi-n02-fixture'


def hashes(tree):
    return {str(p.relative_to(tree)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in sorted(tree.rglob('*')) if p.is_file()}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--core', type=Path, required=True)
    parser.add_argument('--builder', type=Path, required=True)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    output = (args.output or Path(tempfile.mkdtemp(prefix='qi-selective-local-'))).resolve()
    output.mkdir(parents=True, exist_ok=True)
    core = args.core.resolve()
    fixture = output / "unrelated 'fixture"
    shutil.copytree(HERE / 'fixture', fixture)
    before = hashes(fixture)
    env = dict(os.environ, GOWORK='off', GOFLAGS='', GOTOOLCHAIN='local')
    commands = []

    def run(command, name, check=True, **kw):
        result = subprocess.run([str(a) for a in command], cwd=output, env=env,
                                capture_output=True, text=True, timeout=180, **kw)
        (output / (name + '.log')).write_text(result.stdout + result.stderr)
        commands.append({'command': [str(a) for a in command], 'exitCode': result.returncode})
        (output / 'commands.json').write_text(json.dumps(commands, indent=2) + '\n')
        if check and result.returncode:
            raise AssertionError(f'{name}: {result.stdout}{result.stderr}; evidence: {output}')
        return result

    mappings = ['--source', 'github.com/RussellLuo/qi/builtin='+str(core/'internal/build/testdata/defaultbuiltin'), '--source', CORE + '=' + str(core), '--source', PLUGIN + '=' + str(fixture)]

    def build(name, packages, want=None, transform=None, source_args=None):
        manifest = output / (name + '.json')
        value = {'apiVersion': 'qi.build/v1alpha1', 'core': {'version': 'v0.0.0'},
            'plugins': [{'module': PLUGIN, 'version': 'v0.0.0', 'package': PLUGIN + '/' + p,
                         'execution': 'local'} for p in packages]}
        if transform: transform(value)
        manifest.write_text(json.dumps(value))
        binary = output / name
        result = run([args.builder, '--manifest', manifest, '--output', binary, *(mappings if source_args is None else source_args)],
                     'build-' + name, check=want is None)
        assert before == hashes(fixture), 'builder changed input fixture'
        if want:
            assert result.returncode and want in result.stderr, result
            assert not binary.exists() and not Path(str(binary) + '.build').exists()
            return
        return binary

    lean = build('lean', ['base'])
    expanded = build('expanded', ['base', 'optional'])
    repeated = build('repeated', ['optional', 'base'])
    for name in ['main.go', 'internal/compiledplugins/plugins_gen.go', 'go.mod', 'go.sum']:
        assert (Path(str(expanded)+'.build')/'project'/name).read_bytes() == (Path(str(repeated)+'.build')/'project'/name).read_bytes(), name
    for binary in [lean, expanded]:
        record = Path(str(binary) + '.build')
        generated = (record/'project/internal/compiledplugins/plugins_gen.go').read_text()
        assert 'application.Compose(' in generated and CORE+'/boot/application' in generated
        assert 'sdk.Compose' not in generated
        main = (record/'project/main.go').read_text()
        assert 'boot.Run(' in main and CORE+'/boot/launch' in main
        raw = (record/'dependencies.json').read_text()
        assert (PLUGIN+'/optional' in raw) == (binary == expanded)
        assert CORE+'/internal/build' not in raw
    profile = output / "user 'profile.yaml"
    profile.write_text('apiVersion: qi.profile/v1alpha1\nbundles: [fixture]\n')
    # Executable output works without access to the Go toolchain.
    no_go = output/'empty-path'; no_go.mkdir()
    runtime_env = {'PATH': str(no_go), 'HOME': str(output), 'LANG': 'C.UTF-8'}
    env_before = env
    env = runtime_env
    result = run([expanded, '--profile', profile, '--', 'hello', "quoted 'input"], 'task', input='')
    assert result.stdout == "hello quoted 'input\n" and not result.stderr, result
    profile.write_text('apiVersion: qi.profile/v1alpha1\nbundles: [fixture]\ninstances: [{id: optional, plugin: fixture/optional}]\n')
    result = run([lean, '--profile', profile, '--', 'hello'], 'missing-type', check=False, input='')
    assert result.returncode == 1 and 'fixture/optional' in result.stderr and not result.stdout, result
    env = env_before
    for name, packages, want in [
        ('duplicate-plugin', ['base', 'duplicate'], 'duplicate'),
        ('duplicate-bundle', ['base', 'duplicatebundle'], 'duplicate'),
        ('duplicate-jsonl', ['base', 'jsonl'], 'duplicate'),
        ('invalid', ['base', 'invalid'], 'compiled Application validation failed'),
        ('wrong', ['wrong'], 'cannot use'),
        ('missing', ['missing'], 'undefined'),
    ]:
        build(name, packages, want)
    build('unknown-field', ['base'], 'field extra', lambda v: v.update(extra=True))
    build('process', ['base'], 'unsupported', lambda v: v['plugins'][0].update(execution='process'))
    build('duplicate-selection', ['base', 'base'], 'duplicate selected')
    build('conflicting-versions', ['base','optional'], 'conflicting versions', lambda v: v['plugins'][1].update(version='v0.1.0'))
    build('unselected-source', ['base'], 'not selected', source_args=mappings+['--source','example.com/unknown=..'])
    build('duplicate-source', ['base'], 'duplicate source', source_args=mappings+mappings[:2])
    build('source-mismatch', ['base'], 'mismatch', source_args=['--source',CORE+'='+str(fixture)])
    build('source-missing', ['base'], 'source', source_args=['--source',CORE+'='+str(output/'absent')])
    build('source-no-mod', ['base'], 'go.mod', source_args=['--source',CORE+'='+str(no_go)])
    original_mod=(fixture/'go.mod').read_bytes()
    (fixture/'go.mod').write_text(original_mod.decode().replace(CORE+' v0.0.0',CORE+' v0.2.0'))
    before=hashes(fixture)
    build('resolved-upgrade',['base'],'resolved to v0.2.0')
    (fixture/'go.mod').write_bytes(original_mod+b'\nreplace example.com/hidden => ../hidden\n')
    before=hashes(fixture)
    build('implicit-replacement',['base'],'has go.mod replacements')
    (fixture/'go.mod').write_bytes(original_mod);before=hashes(fixture)
    binary_hash=hashlib.sha256(expanded.read_bytes()).hexdigest()
    record_before=hashes(Path(str(expanded)+'.build'))
    existing=run([args.builder,'--manifest',output/'expanded.json','--output',expanded,*mappings],'existing-output',check=False)
    assert existing.returncode and 'refusing existing' in existing.stderr
    assert binary_hash==hashlib.sha256(expanded.read_bytes()).hexdigest() and record_before==hashes(Path(str(expanded)+'.build'))
    stale=output/'stale.build';stale.mkdir();(stale/'sentinel').write_text('preserve')
    existing=run([args.builder,'--manifest',output/'expanded.json','--output',output/'stale',*mappings],'existing-record',check=False)
    assert existing.returncode and (stale/'sentinel').read_text()=='preserve'
    for path in fixture.rglob('*.go'):
        assert '/internal/' not in path.read_text() and CORE+'/sdk' not in path.read_text(), path
    (output/'source-inputs.json').write_text(json.dumps(before, indent=2)+'\n')
    (output/'result.json').write_text(json.dumps({'status':'PASS', 'module':PLUGIN, 'GOWORK':'off',
        'selectedClosure':True, 'deterministic':True, 'atomicFailures':True, 'publicComposeWithBootRun':True,
        'noGoTask':True, 'freshCaches':False}, indent=2)+'\n')
    print(f'Selective build local PASS: selected closure, deterministic generation, atomic failures, public Compose + boot.Run task; evidence: {output}')


if __name__ == '__main__':
    main()
