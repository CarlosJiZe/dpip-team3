# Extraer frames de un video:
#   python3 video_utils.py -action extract video.mp4 frames
#
# Unir frames en un video:
#   python3 video_utils.py -action join filtered.mp4 filtered-frames

import argparse
import glob
import os

import cv2


def extract_frames(video, frames_path):
    vidcap = cv2.VideoCapture(video)
    success, image = vidcap.read()
    count = 0

    if not os.path.isdir(frames_path):
        os.mkdir(frames_path)

    while success:
        cv2.imwrite("{}/{}.png".format(frames_path, count), image)
        success, image = vidcap.read()
        print('Read a new frame: ', count)
        count += 1


def _frame_key(path):
    # Ordena por numero de frame (0.png, 1.png, ...). Si el nombre no es
    # numerico, cae a orden alfabetico para no reventar.
    name = os.path.splitext(os.path.basename(path))[0]
    try:
        return (0, int(name))
    except ValueError:
        return (1, name)


def join_frames(frames_path, output):
    if not os.path.isdir(frames_path):
        print(f"'{frames_path}' frames path doesn't exist")
        return

    frames = glob.glob(f'{frames_path}/*.png')
    if not frames:
        print("No frames found")
        return

    frames.sort(key=_frame_key)

    first = cv2.imread(frames[0])
    if first is None:
        print("Could not read first frame")
        return

    height, width, layers = first.shape
    size = (width, height)

    fourcc = cv2.VideoWriter_fourcc(*'mp4v')
    out = cv2.VideoWriter(output, fourcc, 30.0, size)

    for filename in frames:
        print("Joining frame:", filename)
        img = cv2.imread(filename)
        if img is None:
            print("Skipping bad frame:", filename)
            continue
        out.write(img)

    out.release()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("-action", default="extract", help="extract or join video frames")
    parser.add_argument("video", default="video.mp4", help="input or output video path")
    parser.add_argument("frames_path", default="frames", help="frames path")

    args = parser.parse_args()
    if args.action == 'extract':
        extract_frames(args.video, args.frames_path)
    elif args.action == 'join':
        join_frames(args.frames_path, args.video)
    else:
        parser.print_help()