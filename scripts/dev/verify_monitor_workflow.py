#!/usr/bin/env python3
"""Exercise the actual Go monitor concurrently with Git; synthetic fixtures only."""
import argparse
import json
import os
from pathlib import Path
import resource
import subprocess
import tempfile
import time


def main(binary,output):
    with tempfile.TemporaryDirectory(prefix='laodi-v1-workflow-') as temp:
        root=Path(temp).resolve();home=root/'home';home.mkdir();repo=root/'project';repo.mkdir();evidence=root/'checkpoints';evidence.mkdir();data=root/'state'
        env={'HOME':str(home),'PATH':'/usr/bin:/bin:/opt/homebrew/bin','GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null','GIT_AUTHOR_NAME':'Synthetic','GIT_AUTHOR_EMAIL':'test@example.invalid','GIT_COMMITTER_NAME':'Synthetic','GIT_COMMITTER_EMAIL':'test@example.invalid','GIT_CONFIG_COUNT':'2','GIT_CONFIG_KEY_0':'core.hooksPath','GIT_CONFIG_VALUE_0':str(home),'GIT_CONFIG_KEY_1':'commit.gpgsign','GIT_CONFIG_VALUE_1':'false'}
        def git(*args):return subprocess.run(['/usr/bin/git',*args],cwd=repo,env=env,capture_output=True,text=True,check=True)
        git('init','-q','--template='+str(home));(repo/'app.txt').write_text('baseline\n');git('add','.');git('commit','-qm','synthetic baseline')
        ws='0123456789ab';folder=evidence/ws/'manifests';folder.mkdir(parents=True)
        def manifest(letter):
            value={'schema':'repo_snapshot_manifest/v2','workspaceKey':str(repo),'createdAt':1,'files':[{'path':'.git/objects/SYNTHETIC','sizeBytes':123},{'path':'app.txt','sizeBytes':9}],'stats':{}}
            (folder/(letter*64+'.json')).write_text(json.dumps(value))
        manifest('a')
        cmd=[str(binary),'watch','--root',str(evidence),'--state-dir',str(data),'--build','3.12.3.7463','--interval','1s','--duration','7s']
        watch=subprocess.Popen(cmd,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
        try:
            deadline=time.monotonic()+3
            while not (data/'state.json').exists():
                if time.monotonic()>deadline:raise RuntimeError('watch startup timeout')
                time.sleep(.02)
            git_results=[]
            for i in range(3):
                (repo/'app.txt').write_text(f'change-{i}\n')
                for args in [('status','--porcelain'),('diff','--no-ext-diff'),('add','app.txt'),('commit','-qm',f'change {i}')]:
                    git(*args);git_results.append({'operation':args[0],'success':True})
                if i==1:manifest('b')
                time.sleep(.6)
            expected_head=git('rev-parse','HEAD').stdout.strip();expected_index=(repo/'.git/index').read_bytes()
            stdout,stderr=watch.communicate(timeout=10)
            st=json.loads((data/'state.json').read_text());summary=subprocess.check_output([str(binary),'incidents','--state-dir',str(data),'--format','agent-summary'],env=env,text=True)
            assert watch.returncode==0,stderr
            assert git('rev-parse','HEAD').stdout.strip()==expected_head
            assert (repo/'.git/index').read_bytes()==expected_index
            assert len(st['events'])==2 and st['events'][0]['baseline_existing'] and not st['events'][1]['baseline_existing']
            assert str(repo) not in summary and 'evidence_hash' not in summary and 'SYNTHETIC' not in summary
            restarted=subprocess.run(cmd[:-1]+['2s'],env=env,capture_output=True,text=True,timeout=6)
            st2=json.loads((data/'state.json').read_text());assert restarted.returncode==0 and len(st2['events'])==2
            report={'schema_version':1,'binary':'laodi development build','build_contract':'3.12.3.7463','git_operations':git_results,'monitor_exit':watch.returncode,'baseline_events':1,'new_events':1,'restart_duplicate_events':0,'source_head_index_preserved_after_task':True,'agent_summary_redacted':True,'actual_zcode_started':False,'notifications_sent':False,'all_expectations_met':True,'scope':'Synthetic data shaped from statically inspected installed ZCode code; not a ZCode GUI or server test'}
        finally:
            if watch.poll() is None:watch.terminate();watch.wait(timeout=5)
    output.parent.mkdir(parents=True,exist_ok=True);output.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n');print(json.dumps(report,ensure_ascii=False,indent=2))

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--binary',type=Path,required=True);p.add_argument('--output',type=Path,required=True);a=p.parse_args();main(a.binary.resolve(),a.output)
