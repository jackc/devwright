#!/usr/bin/env python3
"""Test managed Codex escalation in a provisioned Linux VM as dev.

Requires Codex sign-in and consumes model usage. Only the exact disposable Git
workflow receives a one-time approval; no persistent approval rules are added.
"""
import json, os, pathlib, queue, shlex, shutil, subprocess, tempfile, threading, time
home = pathlib.Path.home()
fixture = pathlib.Path(tempfile.mkdtemp(prefix='devwright-approval-', dir=home))
workroot = pathlib.Path(tempfile.mkdtemp(prefix='devwright-approval-', dir=home / '.codex'))
repo, work = fixture / 'repo', workroot / 'worktree'
server = None

def run(*args, **kwargs):
    return subprocess.run(args, text=True, capture_output=True, **kwargs)

def git(*args):
    r = run('git', *map(str,args)); r.check_returncode(); return r.stdout.strip()

try:
    git('init','-q','-b','main',repo)
    for key,value in [('user.name','Devwright test'),('user.email','test@example.invalid'),('commit.gpgsign','false'),('core.hooksPath','/dev/null')]:
        git('-C',repo,'config',key,value)
    git('-C',repo,'commit','-qm','seed','--allow-empty')
    git('-C',repo,'worktree','add','-q','--detach',work)
    workflow=fixture/'workflow.sh'
    workflow.write_text('set -eu\ncd '+shlex.quote(str(work))+'\ngit switch -c codex/regression\nprintf "feature\\n" > feature.txt\ngit add feature.txt\ngit commit -qm feature\ncd '+shlex.quote(str(repo))+'\ngit merge --ff-only codex/regression\ngit worktree remove '+shlex.quote(str(work))+'\n')
    expected='bash '+shlex.quote(str(workflow))
    r=run('codex','sandbox','--include-managed-config','-P',':workspace','-C',str(work),'bash',str(workflow))
    if r.returncode==0 or not ('Read-only file system' in r.stderr or 'Permission denied' in r.stderr or 'unable to create directory' in r.stderr):
        raise RuntimeError('Expected protected Git write denial: '+r.stdout+r.stderr)
    print('PASS default sandbox denies Git metadata writes',flush=True)
    server=subprocess.Popen(['codex','app-server','--stdio','--strict-config'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True)
    messages=queue.Queue()
    def reader():
        for line in server.stdout:
            messages.put(json.loads(line))
        messages.put({'error':'server exited'})
    threading.Thread(target=reader,daemon=True).start()
    def send(m):
        server.stdin.write(json.dumps(m)+'\n');server.stdin.flush()
    approvals=0
    deadline=time.monotonic()+180
    def receive():
        global approvals
        while True:
            m=messages.get(timeout=max(1,deadline-time.monotonic()))
            if 'method' in m and 'id' in m:
                p=m.get('params',{})
                command=p.get('command','')
                # Approve only the exact pre-created workflow. No shell additions,
                # persistent rules, other commands, or other approval types.
                try:
                    parts=shlex.split(command)
                    if len(parts)>=3 and parts[-2] in ('-lc','-c'):
                        command=parts[-1]
                except ValueError:
                    pass
                ok=m['method']=='item/commandExecution/requestApproval' and command==expected and p.get('cwd')==str(work)
                send({'id':m['id'],'result':{'decision':'accept' if ok else 'decline'}})
                if not ok: raise RuntimeError('Unexpected approval: '+json.dumps(p))
                approvals+=1
                print('Approved once: disposable Git workflow',flush=True)
                continue
            return m
    def request(i,method,params):
        send({'id':i,'method':method,'params':params})
        while True:
            m=receive()
            if m.get('id')==i:
                if 'error' in m: raise RuntimeError(str(m['error']))
                return m['result']
    request(1,'initialize',{'clientInfo':{'name':'devwright_approval_test','version':'1.0'},'capabilities':{'experimentalApi':True}})
    send({'method':'initialized'})
    thread=request(2,'thread/start',{'cwd':str(work),'permissions':':workspace','approvalPolicy':'on-request','approvalsReviewer':'user','ephemeral':True})
    tid=thread['thread']['id']
    prompt='This is a disposable permission regression test. Run exactly this command with exec_command and sandbox_permissions=require_escalated, workdir='+str(work)+': '+expected+'. The ordinary sandbox has already denied it. Request approval for this one command. Do not edit the script or execute any other commands. Do not use agents. If the command is blocked after approval, report that and stop.'
    request(3,'turn/start',{'threadId':tid,'input':[{'type':'text','text':prompt}]})
    while True:
        m=receive()
        if m.get('method')=='item/completed':
            item=m['params'].get('item',{})
            if item.get('type')=='commandExecution':
                print('Command exit:',item.get('exitCode'),flush=True)
        if m.get('method')=='turn/completed': break
        if time.monotonic()>deadline: raise TimeoutError('approval test timeout')
    if approvals!=1: raise RuntimeError('Expected exactly one approval, got '+str(approvals))
    if work.exists() or (repo/'feature.txt').read_text()!='feature\n' or git('-C',repo,'rev-parse','main')!=git('-C',repo,'rev-parse','codex/regression'):
        raise RuntimeError('Approved Git workflow did not complete')
    print('PASS actual on-request approval permits branch, commit, merge, and worktree cleanup',flush=True)
finally:
    if server:
        server.terminate()
        try:server.wait(timeout=5)
        except subprocess.TimeoutExpired:server.kill();server.wait()
    shutil.rmtree(fixture)
    shutil.rmtree(workroot)
