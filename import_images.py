import os
import json
import requests
from PIL import Image
from google import genai
from google.genai import types

# =====================================================================
# Homebox AI-Auto-Importer (Gemini 2.5 Flash Lite)
# =====================================================================

# 1. Konfiguration
API_URL = "http://localhost:3100/api/v1"  # Deine Homebox URL
API_TOKEN = "DEIN_HOMEBOX_API_TOKEN"      # Homebox Token (Profil -> API Keys)
GEMINI_API_KEY = "DEIN_GEMINI_API_KEY"    # Erstellen unter aistudio.google.com
FOLDER_PATH = "./bilder"                  # Pfad zu deinen Bildern

# Optional: Zusätzliche Tags, die JEDEM Item hinzugefügt werden (IDs)
EXTRA_TAG_IDS = [] 

# =====================================================================

def fetch_homebox_locations():
    """Ruft alle existierenden Lagerorte aus Homebox ab."""
    headers = {"Authorization": f"Bearer {API_TOKEN}"}
    try:
        response = requests.get(f"{API_URL}/locations", headers=headers)
        if response.status_code == 200:
            return response.json()
        print(f"Warnung: Lagerorte konnten nicht geladen werden ({response.status_code}).")
    except Exception as e:
        print(f"Lagerort-Abruf fehlgeschlagen: {e}")
    return []

def fetch_homebox_tags():
    """Ruft alle existierenden Tags ab, um Namen in IDs aufzulösen."""
    headers = {"Authorization": f"Bearer {API_TOKEN}"}
    try:
        response = requests.get(f"{API_URL}/tags", headers=headers)
        if response.status_code == 200:
            return response.json()
    except:
        pass
    return []

def get_or_create_tag(tag_name, existing_tags, headers):
    """Sucht einen Tag nach Namen oder erstellt ihn neu."""
    tag_name_lower = tag_name.lower().strip()
    for t in existing_tags:
        if t['name'].lower() == tag_name_lower:
            return t['id']
    
    # Neu erstellen
    try:
        res = requests.post(f"{API_URL}/tags", headers=headers, json={"name": tag_name})
        if res.status_code == 201:
            new_tag = res.json()
            existing_tags.append(new_tag)
            return new_tag['id']
    except:
        pass
    return None

def analyze_image_with_gemini(file_path, available_locations):
    """Analysiert das Bild mit Gemini und wählt einen Lagerort aus."""
    client = genai.Client(api_key=GEMINI_API_KEY)
    
    location_names = [loc['name'] for loc in available_locations]
    locations_str = ", ".join(location_names) if location_names else "Keine Lagerorte definiert"

    prompt = f"""Analysiere diesen Gegenstand auf dem Bild und extrahiere Informationen im JSON-Format.
    Wähle aus folgender Liste der verfügbaren Lagerorte den passendsten aus: [{locations_str}]
    
    Antworte mit einem JSON-Objekt:
    - "name": Kurzer Produktname
    - "description": Detaillierte Beschreibung auf Deutsch
    - "manufacturer": Hersteller (falls erkennbar)
    - "model_number": Modellnummer (falls erkennbar)
    - "serial_number": Seriennummer (falls erkennbar)
    - "quantity": Anzahl als Zahl (Standard: 1)
    - "tags": 2 passende Kategorie-Tags (Kurz, Deutsch)
    - "location_name": Der Name des gewählten Lagerorts aus der Liste (exakt so geschrieben!)
    
    Beispiel: {{"name": "Hammer", "description": "Schlosserhammer...", "location_name": "Werkstatt"}}"""

    try:
        image = Image.open(file_path)
        response = client.models.generate_content(
            model="gemini-2.5-flash-lite",
            contents=[prompt, image],
            config=types.GenerateContentConfig(
                response_mime_type="application/json"
            )
        )
        return json.loads(response.text)
    except Exception as e:
        print(f"KI-Analyse fehlgeschlagen für {file_path}: {e}")
        return None

def process_import():
    headers = {"Authorization": f"Bearer {API_TOKEN}"}
    
    print("Initialisiere Homebox-Daten...")
    hb_locations = fetch_homebox_locations()
    location_map = {loc['name'].lower(): loc['id'] for loc in hb_locations}
    hb_tags = fetch_homebox_tags()

    if not os.path.exists(FOLDER_PATH):
        print(f"Ordner {FOLDER_PATH} nicht gefunden.")
        return

    images = [f for f in os.listdir(FOLDER_PATH) if f.lower().endswith(('.png', '.jpg', '.jpeg', '.webp'))]
    print(f"Starte Import von {len(images)} Bildern...\n")

    for filename in images:
        file_path = os.path.join(FOLDER_PATH, filename)
        print(f"--- Verarbeite: {filename} ---")

        ai_data = analyze_image_with_gemini(file_path, hb_locations)
        
        name = ai_data.get("name", os.path.splitext(filename)[0]) if ai_data else os.path.splitext(filename)[0]
        description = ai_data.get("description", "") if ai_data else ""
        location_id = None

        if ai_data and ai_data.get("location_name"):
            location_id = location_map.get(ai_data["location_name"].lower())
            if location_id:
                print(f"  -> Lagerort: {ai_data['location_name']}")

        # Tags auflösen
        tag_ids = list(EXTRA_TAG_IDS)
        if ai_data and ai_data.get("tags"):
            for tn in ai_data["tags"]:
                tid = get_or_create_tag(tn, hb_tags, headers)
                if tid: tag_ids.append(tid)

        # 1. Item erstellen (Basisdaten)
        item_payload = {
            "name": name,
            "description": description,
            "quantity": float(ai_data.get("quantity", 1)) if ai_data else 1.0,
            "locationId": location_id,
            "tagIds": tag_ids
        }
        
        res_item = requests.post(f"{API_URL}/items", headers=headers, json=item_payload)
        if res_item.status_code != 201:
            print(f"  -> Fehler beim Erstellen: {res_item.text}")
            continue

        item_id = res_item.json().get("id")

        # 2. Item per PATCH erweitern (Hersteller, Modell, Serie)
        if ai_data:
            patch_payload = {
                "manufacturer": ai_data.get("manufacturer", ""),
                "modelNumber": ai_data.get("model_number", ""),
                "serialNumber": ai_data.get("serial_number", ""),
            }
            requests.patch(f"{API_URL}/items/{item_id}", headers=headers, json=patch_payload)

        # 3. Bild hochladen
        with open(file_path, "rb") as f:
            files = {"file": f}
            data = {"name": filename, "primary": "true"}
            res_attach = requests.post(f"{API_URL}/items/{item_id}/attachments", headers=headers, files=files, data=data)
            
            if res_attach.status_code == 201:
                print(f"  -> Erfolg: '{name}' importiert.")
            else:
                print(f"  -> Fehler beim Bild-Upload: {res_attach.text}")

if __name__ == "__main__":
    process_import()
