import os,pathlib,subprocess,tempfile,shutil,sys

# Usage: python3 scripts/tests/test_buildbox_run_mirror.py /path/to/buildbox-run
# SSH and df are local fixtures; this test never connects to buildbox.
root=pathlib.Path(tempfile.mkdtemp(prefix='buildbox-mirror-test-'))
remote=root/'remote'; remote.mkdir()
bin=root/'bin'; bin.mkdir()
ssh=bin/'ssh';ssh.write_text('''#!/usr/bin/env python3
import os,sys
args=sys.argv[1:]
while args and args[0].startswith('-'):
 flag=args.pop(0)
 if flag=='-G': sys.exit(0)
 if flag in ('-o','-p','-l','-i'): args.pop(0)
args.pop(0)
os.environ['HOME']=os.environ['TEST_REMOTE_HOME']
os.chdir(os.environ['HOME'])
os.execvp('bash',['bash','-c',' '.join(args)])
''');ssh.chmod(0o755)
df=bin/'df';df.write_text('#!/bin/sh\nprintf "Avail\\n900000000000\\n"\n');df.chmod(0o755)
env=dict(os.environ, PATH=str(bin)+':'+os.environ['PATH'],TEST_REMOTE_HOME=str(remote))
script=str(pathlib.Path(sys.argv[1]).resolve())
def run(args,code=0):
 r=subprocess.run(list(map(str,args)),env=env,text=True,capture_output=True)
 if r.returncode!=code: raise Exception((args,r.returncode,r.stdout,r.stderr))
 return r.stdout.strip()
def git(*args):return run(['git',*args])
repo=root/'repo';repo.mkdir();git('init','-q',repo)
git('-C',repo,'config','user.name','Fixture');git('-C',repo,'config','user.email','fixture@example.test')
(repo/'keep').write_text('committed');(repo/'removed').write_text('remove');(repo/'rename').write_text('rename')
git('-C',repo,'add','.');git('-C',repo,'commit','-qm','fixture')
git('-C',repo,'config','remote.origin.url','https://github.com/fixture/repo.git')
(repo/'keep').write_text('changed');(repo/'removed').unlink();(repo/'rename').rename(repo/'renamed')
(repo/'space and\nnewline').write_text('untracked');(repo/'-dash').write_text('dash')
(repo/'.gitignore').write_text('node_modules/\nignored\n');(repo/'ignored').write_text('ignore')
(repo/'node_modules').mkdir();(repo/'node_modules'/'thing').write_text('dependency')
run([script,repo,'python3','-c',"from pathlib import Path; assert Path('keep').read_text()=='changed'; assert not Path('removed').exists(); assert not Path('rename').exists(); assert Path('renamed').exists(); assert Path('space and\\nnewline').exists(); assert Path('-dash').exists(); assert not Path('ignored').exists(); assert not Path('node_modules/thing').exists()"])
run([script,'--node-modules','--with-git',repo,'test','-f','node_modules/thing'])
run([script,repo,'bash','-c','exit 37'],37)
# A second source snapshot must reuse the same mirror.
clone=root/'clone';git('clone','-q',repo,clone);git('-C',clone,'config','buckley.reviewRepository','github.com/fixture/repo')
run([script,'--sync-only',clone]);assert len(list((remote/'mirrors').glob('*.git')))==1
run([script,'--cleanup',repo]);run([script,'--cleanup',clone]);run([script,'--cleanup',clone])
assert not list((remote/'work').glob('*/.git'))
mirror=next((remote/'mirrors').glob('*.git'))
assert len(git('--git-dir',mirror,'worktree','list','--porcelain').split('worktree '))==2
plain=root/'plain';plain.mkdir();(plain/'a').write_text('plain');(plain/'link').symlink_to('a')
run([script,'--with-git',plain,'test','-L','link']);run([script,'--sync-only',plain]);run([script,'--cleanup',plain])
# A plain copy at the same path must not be replaced without cleanup.
run([script,'--sync-only',plain]);git('init','-q',plain)
git('-C',plain,'-c','user.name=Fixture','-c','user.email=fixture@example.test','add','a')
git('-C',plain,'-c','user.name=Fixture','-c','user.email=fixture@example.test','commit','-qm','fixture')
run([script,'--sync-only',plain],90);run([script,'--cleanup',plain])
df.write_text('#!/bin/sh\nprintf "Avail\\n60000000000\\n"\n')
run([script,'--sync-only',repo],91)
print('PASS: mirror reuse, overlays, NUL names, options, exits 37/90/91, cleanup, and plain directories')
shutil.rmtree(root)
