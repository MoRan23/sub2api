import json
from pathlib import Path
import subprocess
import time

def linux_path(path):
    value = str(path.resolve()).replace('\\', '/')
    return '/mnt/' + value[0].lower() + value[2:] if len(value) > 2 and value[1] == ':' else value

base = Path(__file__).resolve().parents[4] / '.git/task-artifacts/merge-v0220'
phases = {
    'cache-extended': ('repository', '^BenchmarkSchedulerCacheReadSharing$', '1000x'),
    'sse-multiline-extended': ('service', '^BenchmarkCodexEngineRelay$/^WS$/^multiline_128$', '1000x'),
}
runs = []
for phase, (package, pattern, iterations) in phases.items():
    for number in range(1, 4):
        for label in ['baseline', 'candidate']:
            stem = f'{label}-{phase}-{number}'
            binary = linux_path(base / (label + '-' + package + '.test'))
            resources = linux_path(base / (stem + '-resources.txt'))
            command = ['wsl.exe', '-d', 'Ubuntu-24.04', '--exec', '/usr/bin/time', '-v', '-o', resources,
                       'env', 'GOMAXPROCS=4', 'GIN_MODE=release', binary, '-test.run=^$',
                       '-test.bench='+pattern, '-test.benchmem', '-test.benchtime='+iterations,
                       '-test.count=1', '-test.timeout=20m']
            print('Running '+stem, flush=True)
            start = time.monotonic()
            with (base/(stem+'.txt')).open('w', encoding='utf8') as out:
                code = subprocess.run(command, stdout=out, stderr=subprocess.STDOUT).returncode
            runs.append({'phase': phase, 'round': number, 'label': label, 'exit_code': code,
                         'elapsed_seconds': round(time.monotonic()-start, 3)})
            (base/'extended-benchmark-runs.json').write_text(json.dumps(runs, indent=2)+'\n')
            print(runs[-1], flush=True)
            if code:
                raise SystemExit(code)
