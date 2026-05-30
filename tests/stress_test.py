# Stress test automation (cross-platform)
#
# Usa una sesion HTTP persistente para reutilizar conexiones TCP y evitar
# el agotamiento de puertos efimeros del lado del cliente.
#
# Empujar imagenes desde un directorio (manda la seq = numero de frame):
#   python3 stress_test.py -action push -workload-id <id> -token <token> -frames-path frames
#
# Descargar imagenes filtradas (las nombra por seq para conservar el orden):
#   python3 stress_test.py -action pull -workload-id <id> -image-type filtered -token <token> -frames-path filtered-frames

import argparse
import glob
import json
import os
import time

import requests

WORKLOADS_API_ENDPOINT = 'http://localhost:8080/workloads'
IMAGES_API_ENDPOINT = 'http://localhost:8080/images'


def push_images(frames_path, workload_id, token):
    if not os.path.isdir(frames_path):
        print("[{}] frames path doesn't exist".format(frames_path))
        return

    frames = glob.glob('{}/*.png'.format(frames_path))
    headers = {'Authorization': 'Bearer {}'.format(token)}

    # Sesion persistente: reutiliza conexiones en vez de abrir una nueva por imagen.
    session = requests.Session()

    sent, failed = 0, 0
    start = time.time()

    for count in range(0, len(frames)):
        image_path = '{}/{}.png'.format(frames_path, count)
        if not os.path.exists(image_path):
            continue
        # 'seq' es el numero de frame; viaja por todo el sistema para reconstruir
        # el orden del video al final.
        data = {'workload_id': workload_id, 'type': 'original', 'seq': str(count)}
        try:
            with open(image_path, 'rb') as f:
                files = {'data': f}
                r = session.post(IMAGES_API_ENDPOINT, files=files, headers=headers, data=data)
            if r.status_code == 200:
                sent += 1
            else:
                failed += 1
                print('Failed {} -> {} {}'.format(image_path, r.status_code, r.text))
        except Exception as e:
            failed += 1
            print('Error sending frame {}: {}'.format(image_path, e))
            continue

    elapsed = time.time() - start
    print('---')
    print('Pushed: {} ok, {} failed in {:.2f}s ({:.1f} img/s)'.format(
        sent, failed, elapsed, sent / elapsed if elapsed > 0 else 0))


def pull_filtered(frames_path, workload_id, image_type, token):
    if not os.path.isdir(frames_path):
        os.mkdir(frames_path)

    headers = {'Authorization': 'Bearer {}'.format(token)}
    session = requests.Session()

    r = session.get(IMAGES_API_ENDPOINT, headers=headers)
    images_info = json.loads(r.text)

    downloaded = 0
    start = time.time()

    for image in images_info:
        if image.get('type') == 'filtered':
            image_url = '{}/{}'.format(IMAGES_API_ENDPOINT, image['image_id'])
            # Nombrar por 'seq' (numero de frame) para conservar el orden original.
            seq = image.get('seq', downloaded)
            image_path = '{}/{}.png'.format(frames_path, seq)
            r = session.get(image_url, allow_redirects=True, headers=headers)
            if r.status_code == 200:
                with open(image_path, 'wb') as f:
                    f.write(r.content)
                downloaded += 1

    elapsed = time.time() - start
    print('---')
    print('Downloaded: {} filtered images in {:.2f}s'.format(downloaded, elapsed))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('-action', default='push', help='push or pull')
    parser.add_argument('-workload-id', default='test', help='Workload identifier')
    parser.add_argument('-image-type', default='filtered', help='filtered or original')
    parser.add_argument('-token', default='token', help='API Token')
    parser.add_argument('-frames-path', default='frames', help='frames path')

    args = parser.parse_args()
    if args.action == 'push':
        push_images(args.frames_path, args.workload_id, args.token)
    elif args.action == 'pull':
        pull_filtered(args.frames_path, args.workload_id, args.image_type, args.token)
    else:
        parser.print_help()