"""Boot-owned process assertions; uses only its locally built fixture."""
import json
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time

binary = str(Path(sys.argv[1]).resolve())
with tempfile.TemporaryDirectory(prefix='qi-boot-process-') as temporary:
    root = Path(temporary)
    def effects(cwd):
        path = cwd/'effects'
        return path.read_text().splitlines() if path.exists() else []
    def launch(mode):
        cwd = root/mode
        cwd.mkdir()
        profile = root/(mode+'.json')
        profile.write_text(json.dumps({'instances':[{'id':'task','plugin':'fixture.task','config':{'mode':mode}}]}))
        return cwd, subprocess.Popen([binary,'--profile',str(profile),'--','task'],cwd=cwd,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
    def wait(value,cwd,p):
        end = time.monotonic()+8
        while value not in effects(cwd):
            assert p.poll() is None, (value,p.communicate(),effects(cwd))
            assert time.monotonic()<end, effects(cwd)
            time.sleep(.01)
    for mode in ['success','cleanup-error','startup-error','cleanup-hang','startup-hang']:
        cwd,p = launch(mode)
        try:
            out,err = p.communicate(timeout=8)
            assert p.returncode == (0 if mode in ['success','cleanup-hang'] else 1), (mode,p.returncode,out,err)
            trace = effects(cwd)
            assert ('started' in trace) == (not mode.startswith('startup')), (mode,trace)
            assert out == ('done\n' if not mode.startswith('startup') else ''), (mode,out)
            assert 'disposing' in trace, (mode,trace)
            assert ('disposed' in trace) == (mode=='success'), (mode,trace)
            if mode.endswith('error'): assert 'fixture cleanup failure' in err, (mode,err)
        finally:
            if p.poll() is None: p.kill();p.wait()
    for mode in ['wait','drain']:
        cwd,p = launch(mode)
        try:
            wait('started',cwd,p)
            p.send_signal(signal.SIGTERM)
            wait('cancelled',cwd,p)
            if mode=='drain':
                assert 'disposing' not in effects(cwd), effects(cwd)
                p.send_signal(signal.SIGINT)
                p.wait(timeout=2)
                assert p.returncode == 130 and 'disposing' not in effects(cwd), (p.returncode,effects(cwd))
            else:
                out,err=p.communicate(timeout=3)
                assert p.returncode == 0 and not out and not err, (p.returncode,out,err)
                trace=effects(cwd)
                assert trace.index('drained') < trace.index('disposing') < trace.index('disposed'), trace
        finally:
            if p.poll() is None:p.kill();p.wait()
    cwd=root/'input-hang'
    cwd.mkdir()
    p=subprocess.Popen([binary,'--profile',str(root/'success.json'),'--','input-hang'],cwd=cwd,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
    try:
        wait('preparing',cwd,p)
        p.send_signal(signal.SIGTERM)
        time.sleep(.1)
        assert p.poll() is None, p.returncode
        p.send_signal(signal.SIGINT)
        p.wait(timeout=2)
        assert p.returncode==130 and effects(cwd)==['preparing'], (p.returncode,effects(cwd))
        assert p.stdout.read()=='' and p.stderr.read()==''
    finally:
        if p.poll() is None:p.kill();p.wait()
        p.stdin.close()
    # Both help paths stay pure; task output failure still drains the Host.
    for index,args in enumerate([['--help'],['--profile',str(root/'success.json'),'--','--help'],['--profile',str(root/'success.json'),'--','task']]):
        cwd=root/('epipe-'+str(index))
        cwd.mkdir()
        import os
        read_fd,write_fd=os.pipe()
        os.close(read_fd)
        try:
            p=subprocess.Popen([binary,*args],cwd=cwd,stdin=subprocess.DEVNULL,stdout=write_fd,stderr=subprocess.PIPE,text=True)
        finally:
            os.close(write_fd)
        try:
            _,err=p.communicate(timeout=3)
            assert p.returncode==1 and 'broken pipe' in err, (p.returncode,err)
            assert effects(cwd)==([] if index<2 else ['started','disposing','disposed']), effects(cwd)
        finally:
            if p.poll() is None:p.kill();p.wait()
print('PASS: boot.Run task, startup rollback, cleanup error/hang grace, cancellation drain before Host disposal, second signal, pre-Host blocked stdin and EPIPE')
