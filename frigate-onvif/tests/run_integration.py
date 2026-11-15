#!/usr/bin/env python3
import subprocess
import time
import os
import sys

def parse_credentials(filepath):
    with open(filepath, 'r') as f:
        lines = [line.strip() for line in f if line.strip()]
    
    cameras = []
    for i in range(0, len(lines), 3):
        if i + 2 < len(lines):
            # Remove 'open' prefix from host if present
            host = lines[i]
            if host.startswith('open'):
                host = host[4:]
            
            cameras.append({
                'host': host,
                'user': lines[i+1],
                'password': lines[i+2]
            })
    return cameras

def run_command(cmd, env=None, cwd=None):
    print(f"Executing: {' '.join(cmd)}")
    return subprocess.run(cmd, env=env, cwd=cwd)

def main():
    # We assume we are running from the root of the repository
    cred_file = 'camera-credentials.txt'
    if not os.path.exists(cred_file):
        print(f"Error: {cred_file} not found in current directory.")
        sys.exit(1)

    cameras = parse_credentials(cred_file)
    print(f"Found {len(cameras)} cameras in {cred_file}")

    # Ensure images are built
    print("==> Building images (this may take a while)...")
    # Build the bridge
    run_command(["docker", "build", "--platform", "linux/amd64", "-t", "ptz-onvif:test", "-f", "frigate-onvif/Dockerfile", "."])
    # Build the test client
    run_command(["docker", "build", "--platform", "linux/amd64", "-t", "ptz-onvif-test-client", "-f", "frigate-onvif/tests/Dockerfile", "frigate-onvif/tests"])

    for idx, cam in enumerate(cameras):
        print(f"\n{'='*40}")
        print(f"TESTING CAMERA {idx+1}/{len(cameras)}: {cam['host']}")
        print(f"{'='*40}")
        
        bridge_name = f"bridge-cam-{idx}"
        
        # Start bridge
        print(f"Starting bridge container {bridge_name}...")
        try:
            subprocess.run([
                "docker", "run", "-d", "--name", bridge_name, 
                "--platform", "linux/amd64",
                "-p", "8000:8000", # Expose port to host
                "-e", f"CAMERA_HOST={cam['host']}",
                "-e", f"CAMERA_PORT=8000",
                "-e", f"CAMERA_USER={cam['user']}",
                "-e", f"CAMERA_PASSWORD={cam['password']}",
                "-e", f"ONVIF_USER=onvif",
                "-e", f"ONVIF_PASSWORD=secret",
                "ptz-onvif:test"
            ], check=True)

            # Wait for health
            print("Waiting for bridge to be healthy (max 60s)...")
            healthy = False
            for _ in range(60):
                res = subprocess.run(
                    ["docker", "inspect", "-f", "{{.State.Health.Status}}", bridge_name],
                    capture_output=True, text=True
                )
                status = res.stdout.strip()
                if status == "healthy":
                    healthy = True
                    break
                if status == "unhealthy":
                    print("Bridge became unhealthy!")
                    break
                time.sleep(1)

            if not healthy:
                print(f"FAILED: Bridge for {cam['host']} failed to become healthy.")
                subprocess.run(["docker", "logs", bridge_name])
                continue

            # Run test client (using host network to reach localhost:8000)
            print("Running ONVIF client tests...")
            client_res = subprocess.run([
                "docker", "run", "--rm", "--platform", "linux/amd64",
                "--network", "host", 
                "-e", "ONVIF_HOST=127.0.0.1",
                "-e", "ONVIF_PORT=8000",
                "-e", "ONVIF_USER=onvif",
                "-e", "ONVIF_PASSWORD=secret",
                "ptz-onvif-test-client"
            ])

            if client_res.returncode == 0:
                print(f"✅ SUCCESS: Camera {cam['host']} passed all tests!")
            else:
                print(f"❌ FAILED: Camera {cam['host']} failed tests.")

        except Exception as e:
            print(f"⚠️ ERROR during test for {cam['host']}: {e}")
        finally:
            print(f"Cleaning up bridge {bridge_name}...")
            subprocess.run(["docker", "rm", "-f", bridge_name], capture_output=True)

    print("\nAll tests finished.")

if __name__ == "__main__":
    main()
