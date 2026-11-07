import json
import os
from pathlib import Path
import subprocess
import time

def linux_path(path):
    value = str(path.resolve()).replace('\\', '/')
    return '/mnt/' + value[0].lower() + value[2:] if len(value) > 2 and value[1] == ':' else value

root = Path(__file__).resolve().parents[4]
artifacts = root / '.git/task-artifacts/merge-v0220'
env = dict(os.environ, GOEXPERIMENT='greenteagc,jsonv2', GOMAXPROCS='4')
phases = {
    'cache': ('repository', '^BenchmarkSchedulerCacheReadSharing$', '100x'),
    'sse': ('service', '^BenchmarkCodexEngineRelay$', '20x'),
    'billing': ('service', '^BenchmarkUsageRecordQueueRetainedHeap$', '1x'),
}
manifest = {
    'baseline': 'b41bb98fb7313b33d7c18bc0e72d6e3d7db86094',
    'upstream': '6b4b37db43443b5e88e424fe3c69602cc3810dfb',
    'candidate': 'merge working tree including compatibility fixes',
    'go': subprocess.check_output(['go', 'env', 'GOVERSION'], cwd=root/'backend', env=env, text=True).strip(),
    'goexperiment': env['GOEXPERIMENT'], 'gomaxprocs': 4,
    'phases': phases, 'rounds': 3, 'order': 'alternating baseline/candidate per phase and round',
    'os': 'Linux amd64, WSL Ubuntu-24.04',
}
if manifest['go'] != 'go1.27.3':
    raise SystemExit('unexpected Go toolchain')
(artifacts/'benchmark-manifest.json').write_text(json.dumps(manifest, indent=2)+'\n')
runs = []
for phase, (package, pattern, iterations) in phases.items():
    for number in range(1, 4):
        for label in ['baseline', 'candidate']:
            stem = f'{label}-{phase}-{number}'
            binary = linux_path(artifacts / (label + '-' + package + '.test'))
            resources = linux_path(artifacts / (stem + '-resources.txt'))
            command = ['wsl.exe', '-d', 'Ubuntu-24.04', '--exec', '/usr/bin/time', '-v', '-o', resources,
                       'env', 'GOMAXPROCS=4', 'GIN_MODE=release', binary, '-test.run=^$',
                       '-test.bench='+pattern, '-test.benchmem', '-test.benchtime='+iterations,
                       '-test.count=1', '-test.timeout=20m']
            print('Running ' + stem, flush=True)
            start = time.monotonic()
            with (artifacts/(stem+'.txt')).open('w', encoding='utf8') as output:
                result = subprocess.run(command, cwd=root/'backend', env=env, stdout=output, stderr=subprocess.STDOUT)
            record = {'phase': phase, 'round': number, 'label': label, 'exit_code': result.returncode,
                      'elapsed_seconds': round(time.monotonic()-start, 3)}
            runs.append(record)
            (artifacts/'benchmark-runs.json').write_text(json.dumps(runs, indent=2)+'\n')
            print(record, flush=True)
            if result.returncode:
                raise SystemExit('benchmark failed: '+stem)
